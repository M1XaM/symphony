// Package library keeps the topologies the user saves as templates. The
// built-in ones are Go literals in spec.Presets(); these are the ones added at
// runtime, held in a single JSON file under the user's config directory so
// they outlive the process.
package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"symphony/engine/spec"
)

// Version is the schema version written into the templates file.
const Version = 1

// FileName is the file written inside the application config directory.
const FileName = "templates.json"

var errNoPath = errors.New("library: no config directory on this machine, templates cannot be stored")

// file is the on-disk envelope. The version is there so a later format can be
// migrated rather than guessed at.
type file struct {
	Version   int             `json:"version"`
	Templates []spec.Topology `json:"templates"`
}

// Library is the set of user templates backed by one JSON file. Every method
// reads and writes the file, so the disk stays the single source of truth and
// two windows cannot drift apart. It is safe for concurrent use.
type Library struct {
	mu   sync.Mutex
	path string
}

// DefaultPath is <user config dir>/symphony/templates.json.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("library: locate config directory: %w", err)
	}
	return filepath.Join(dir, "symphony", FileName), nil
}

// New returns a library stored at path. An empty path (DefaultPath failed)
// yields a library that reports an error from every call rather than
// pretending to save.
func New(path string) *Library {
	return &Library{path: path}
}

// Path returns the file the library is stored in, "" if it has none.
func (l *Library) Path() string { return l.path }

// List returns the saved templates in the order they were added. A missing
// file is not an error: it means nothing has been saved yet.
func (l *Library) List() ([]spec.Topology, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.load()
}

// Save adds a template, replacing any existing one of the same name, and
// returns the library as it now stands on disk.
func (l *Library) Save(t spec.Topology) ([]spec.Topology, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	saved := t.Normalized()
	if saved.Name == "" {
		return nil, errors.New("library: a template needs a name")
	}
	list, err := l.load()
	if err != nil {
		return nil, err
	}
	replaced := false
	for i := range list {
		if list[i].Name == saved.Name {
			list[i] = saved
			replaced = true
			break
		}
	}
	if !replaced {
		list = append(list, saved)
	}
	if err := l.store(list); err != nil {
		return nil, err
	}
	return list, nil
}

// Delete removes the named template and returns what is left. Deleting a name
// that is not there is a no-op, not an error.
func (l *Library) Delete(name string) ([]spec.Topology, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	list, err := l.load()
	if err != nil {
		return nil, err
	}
	kept := make([]spec.Topology, 0, len(list))
	for _, t := range list {
		if t.Name != name {
			kept = append(kept, t)
		}
	}
	if len(kept) == len(list) {
		return kept, nil
	}
	if err := l.store(kept); err != nil {
		return nil, err
	}
	return kept, nil
}

func (l *Library) load() ([]spec.Topology, error) {
	if l.path == "" {
		return nil, errNoPath
	}
	b, err := os.ReadFile(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return []spec.Topology{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("library: read %s: %w", l.path, err)
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("library: parse %s: %w", l.path, err)
	}
	out := make([]spec.Topology, 0, len(f.Templates))
	for _, t := range f.Templates {
		out = append(out, t.Normalized())
	}
	return out, nil
}

func (l *Library) store(list []spec.Topology) error {
	if l.path == "" {
		return errNoPath
	}
	dir := filepath.Dir(l.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("library: create %s: %w", dir, err)
	}
	b, err := json.MarshalIndent(file{Version: Version, Templates: list}, "", "  ")
	if err != nil {
		return fmt.Errorf("library: encode templates: %w", err)
	}
	b = append(b, '\n')

	// Write beside the target and rename, so an interrupted save cannot leave
	// a truncated library behind.
	tmp, err := os.CreateTemp(dir, ".templates-*.json")
	if err != nil {
		return fmt.Errorf("library: temp file in %s: %w", dir, err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("library: write %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("library: close %s: %w", name, err)
	}
	if err := os.Rename(name, l.path); err != nil {
		os.Remove(name)
		return fmt.Errorf("library: rename onto %s: %w", l.path, err)
	}
	return nil
}
