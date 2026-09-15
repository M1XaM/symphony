// Package images makes sure the block images exist: it builds them from an
// embedded copy of src/blocks (so a fresh install needs only Docker) or
// pulls third-party ones.
package images

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"sync"
	"time"

	"symphony/engine/runtime"
	"symphony/engine/spec"
)

// Dockerfile is the path of the blocks Dockerfile inside the build context.
const Dockerfile = "blocks/Dockerfile"

// BlockFor maps a symphony/<block>:tag image to its block name.
func BlockFor(image string) (string, bool) {
	if !spec.IsBlockImage(image) {
		return "", false
	}
	rest := strings.TrimPrefix(image, "symphony/")
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		rest = rest[:i]
	}
	switch rest {
	case "gateway", "service", "broker", "database", "proxy":
		return rest, true
	}
	return "", false
}

// BuildContext tars go.mod, go.sum and blocks/** from fsys (the embedded
// source tree), leaving tests and testdata out.
func BuildContext(fsys fs.FS) (io.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(name string) error {
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(b)), ModTime: time.Unix(0, 0)}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err = tw.Write(b)
		return err
	}
	for _, f := range []string{"go.mod", "go.sum"} {
		if err := add(f); err != nil {
			return nil, fmt.Errorf("build context: %w", err)
		}
	}
	err := fs.WalkDir(fsys, "blocks", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path.Base(p) == "testdata" {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, "_test.go") {
			return nil
		}
		return add(p)
	})
	if err != nil {
		return nil, fmt.Errorf("build context: %w", err)
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}

// Ensurer serialises image preparation so two deployments applying at once
// do not build the same image twice.
type Ensurer struct {
	Source fs.FS // embedded src tree; nil disables building
	mu     sync.Mutex
	locks  map[string]*sync.Mutex
}

func NewEnsurer(src fs.FS) *Ensurer {
	return &Ensurer{Source: src, locks: map[string]*sync.Mutex{}}
}

func (e *Ensurer) lock(ref string) *sync.Mutex {
	e.mu.Lock()
	defer e.mu.Unlock()
	m := e.locks[ref]
	if m == nil {
		m = &sync.Mutex{}
		e.locks[ref] = m
	}
	return m
}

// Ensure makes ref available in the runtime: present already, built from
// the embedded blocks source for symphony/* images, pulled otherwise.
func (e *Ensurer) Ensure(ctx context.Context, rt runtime.Runtime, ref string, progress func(string)) error {
	m := e.lock(ref)
	m.Lock()
	defer m.Unlock()
	ok, err := rt.ImageExists(ctx, ref)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	if block, isBlock := BlockFor(ref); isBlock {
		if e.Source == nil {
			return fmt.Errorf("image %s is missing and this build has no embedded block sources; run `make blocks`", ref)
		}
		progress("building " + ref + " from the embedded blocks source")
		bc, err := BuildContext(e.Source)
		if err != nil {
			return err
		}
		start := time.Now()
		if err := rt.BuildImage(ctx, ref, bc, Dockerfile, map[string]string{"BLOCK": block}, progress); err != nil {
			return fmt.Errorf("build %s: %w", ref, err)
		}
		progress(fmt.Sprintf("built %s in %s", ref, time.Since(start).Round(time.Second)))
		return nil
	}
	progress("pulling " + ref)
	if err := rt.PullImage(ctx, ref, progress); err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	return nil
}
