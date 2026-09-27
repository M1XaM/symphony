package store

import (
	"os"
	"path/filepath"
	"testing"

	"symphony/engine/spec"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "sub", FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestInstanceIDIsStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.InstanceID()
	if err != nil || len(a) != 12 {
		t.Fatalf("id %q, %v", a, err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if b, _ := s.InstanceID(); b != a {
		t.Fatalf("id changed across opens: %q -> %q", a, b)
	}
}

func TestDeploymentRoundTripAndRevisions(t *testing.T) {
	s := open(t)
	topo := spec.Presets()[0]
	d := Deployment{Name: topo.Name, Slug: spec.Slug(topo.Name), Token: "tok", Network: "net", Phase: "applying", Topology: topo}
	rev, err := s.Save(d)
	if err != nil || rev != 1 {
		t.Fatalf("first save: rev %d, %v", rev, err)
	}
	if rev, _ = s.SetPhase(d.Slug, "running"); rev != 2 {
		t.Fatalf("SetPhase rev %d, want 2", rev)
	}
	load := map[string]map[string]any{"gateway": {"rps": 20.0}}
	if rev, _ = s.SetLoad(d.Slug, load); rev != 3 {
		t.Fatalf("SetLoad rev %d, want 3", rev)
	}
	got, ok, err := s.Deployment(d.Slug)
	if err != nil || !ok {
		t.Fatalf("Deployment: %v %v", ok, err)
	}
	if got.Phase != "running" || got.Rev != 3 || got.Token != "tok" || got.Load["gateway"]["rps"] != 20.0 {
		t.Fatalf("read back %+v", got)
	}
	if len(got.Topology.Nodes) != len(topo.Nodes) || len(got.Topology.Edges) != len(topo.Edges) {
		t.Fatalf("topology lost nodes or edges")
	}
	if rev, _ = s.SetLoad(d.Slug, nil); rev != 4 {
		t.Fatalf("clearing the load: rev %d", rev)
	}
	if got, _, _ = s.Deployment(d.Slug); got.Load != nil {
		t.Fatalf("load not cleared: %v", got.Load)
	}
	if rev, _ = s.SetPhase("nope", "running"); rev != 0 {
		t.Fatalf("SetPhase on a missing deployment returned rev %d", rev)
	}
	if err := s.Delete(d.Slug); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.Deployments(); len(all) != 0 {
		t.Fatalf("still stored after delete: %v", all)
	}
}

func TestNilStoreRemembersNothing(t *testing.T) {
	var s *Store
	if _, err := s.Save(Deployment{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	if all, err := s.Deployments(); err != nil || all != nil {
		t.Fatalf("%v %v", all, err)
	}
	if id, err := s.InstanceID(); err != nil || id == "" {
		t.Fatalf("%q %v", id, err)
	}
}

func TestImportLegacy(t *testing.T) {
	cfg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfg, "deployments"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(cfg, "instance"), []byte("0123456789ab"), 0o644)
	os.WriteFile(filepath.Join(cfg, "deployments", "my-saga.json"),
		[]byte(`{"name":"My saga","token":"t","network":"symphony-my-saga","topology":{"version":1,"name":"My saga","nodes":[],"edges":[]}}`), 0o600)

	s := open(t)
	n, err := s.ImportLegacy(cfg)
	if err != nil || n != 1 {
		t.Fatalf("imported %d, %v", n, err)
	}
	if id, _ := s.InstanceID(); id != "0123456789ab" {
		t.Fatalf("instance id %q not taken over", id)
	}
	d, ok, _ := s.Deployment("my-saga")
	if !ok || d.Name != "My saga" || d.Phase != "running" {
		t.Fatalf("imported %+v", d)
	}
	if n, _ := s.ImportLegacy(cfg); n != 0 {
		t.Fatalf("second import brought %d more", n)
	}
}
