package images

import (
	"context"
	"io"
	"testing"
	"testing/fstest"

	"symphony/blocks/common/wire"
	"symphony/engine/runtime"
)

// fakeRuntime answers only what Ensure asks.
type fakeRuntime struct {
	runtime.Runtime
	labels map[string]map[string]string
	built  []string
	args   map[string]string
	pulled []string
}

func (f *fakeRuntime) ImageExists(_ context.Context, ref string) (bool, error) {
	_, ok := f.labels[ref]
	return ok, nil
}

func (f *fakeRuntime) ImageLabels(_ context.Context, ref string) (map[string]string, bool, error) {
	l, ok := f.labels[ref]
	return l, ok, nil
}

func (f *fakeRuntime) BuildImage(_ context.Context, ref string, _ io.Reader, _ string, args map[string]string, _ func(string)) error {
	f.built = append(f.built, ref)
	f.args = args
	return nil
}

func (f *fakeRuntime) PullImage(_ context.Context, ref string, _ func(string)) error {
	f.pulled = append(f.pulled, ref)
	return nil
}

var source = fstest.MapFS{
	"go.mod":            {Data: []byte("module symphony\n")},
	"go.sum":            {Data: nil},
	"blocks/Dockerfile": {Data: []byte("FROM scratch\n")},
}

func TestEnsureBlockImages(t *testing.T) {
	ctx := context.Background()
	quiet := func(string) {}
	rt := &fakeRuntime{labels: map[string]map[string]string{
		"symphony/service:dev": {ContractLabel: wire.Contract},
		"symphony/broker:dev":  {ContractLabel: "v0.0.1"},
		"nginx:1":              {},
	}}
	e := NewEnsurer(source)
	for _, ref := range []string{"symphony/service:dev", "symphony/broker:dev", "symphony/gateway:dev", "nginx:1", "redis:7"} {
		if err := e.Ensure(ctx, rt, ref, quiet); err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
	}
	// a current image is kept, an outdated and a missing one are built
	if len(rt.built) != 2 || rt.built[0] != "symphony/broker:dev" || rt.built[1] != "symphony/gateway:dev" {
		t.Fatalf("built %v", rt.built)
	}
	if rt.args["CONTRACT"] != wire.Contract || rt.args["BLOCK"] != "gateway" {
		t.Fatalf("build args %v", rt.args)
	}
	if len(rt.pulled) != 1 || rt.pulled[0] != "redis:7" {
		t.Fatalf("pulled %v", rt.pulled)
	}

	// an image the app built from other sources is rebuilt; one built by
	// hand (no source label) or from these sources is kept
	digest := e.sourceDigest()
	if digest == "" || rt.args["SOURCE"] != digest {
		t.Fatalf("digest %q, build args %v", digest, rt.args)
	}
	rt.built = nil
	rt.labels["symphony/proxy:dev"] = map[string]string{ContractLabel: wire.Contract, SourceLabel: "0123456789abcdef"}
	rt.labels["symphony/database:dev"] = map[string]string{ContractLabel: wire.Contract, SourceLabel: digest}
	rt.labels["symphony/service:dev"] = map[string]string{ContractLabel: wire.Contract, SourceLabel: ""}
	for _, ref := range []string{"symphony/proxy:dev", "symphony/database:dev", "symphony/service:dev"} {
		if err := e.Ensure(ctx, rt, ref, quiet); err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
	}
	if len(rt.built) != 1 || rt.built[0] != "symphony/proxy:dev" {
		t.Fatalf("built %v, want only the proxy", rt.built)
	}

	// without sources an existing image is used as it is, a missing one is an error
	rt.built = nil
	e = NewEnsurer(nil)
	if err := e.Ensure(ctx, rt, "symphony/broker:dev", quiet); err != nil || len(rt.built) != 0 {
		t.Fatalf("outdated image without sources: %v, built %v", err, rt.built)
	}
	if err := e.Ensure(ctx, rt, "symphony/gateway:dev", quiet); err == nil {
		t.Fatal("a missing image without sources must be an error")
	}
}
