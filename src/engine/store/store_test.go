package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestFaults(t *testing.T) {
	s := open(t)
	topo := spec.Presets()[0]
	d := Deployment{Name: topo.Name, Slug: spec.Slug(topo.Name), Token: "tok", Network: "net", Phase: "running", Topology: topo}
	if _, err := s.Save(d); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Minute).Truncate(time.Millisecond)
	id1, rev, err := s.AddFault(d.Slug, Fault{Node: "payment", Kind: "pause", Until: until})
	if err != nil || id1 == 0 || rev != 2 {
		t.Fatalf("AddFault: id %d rev %d, %v", id1, rev, err)
	}
	id2, _, _ := s.AddFault(d.Slug, Fault{Node: "payment", Kind: "latency", BlockID: "f-1", Params: map[string]any{"ms": 250.0}})
	if _, _, err := s.AddFault(d.Slug, Fault{Node: "inventory", Kind: "kill"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Faults(d.Slug)
	if err != nil || len(got) != 3 {
		t.Fatalf("Faults: %v, %v", got, err)
	}
	if !got[0].Until.Equal(until) || !got[1].Until.IsZero() || got[1].BlockID != "f-1" || got[1].Params["ms"] != 250.0 {
		t.Fatalf("read back %+v", got)
	}
	if rev, _ := s.RemoveFault(d.Slug, id2); rev != 5 {
		t.Fatalf("RemoveFault rev %d, want 5", rev)
	}
	if _, err := s.ClearFaults(d.Slug, "payment"); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.Faults(d.Slug); len(got) != 1 || got[0].Node != "inventory" {
		t.Fatalf("after clearing payment: %+v", got)
	}
	if err := s.Delete(d.Slug); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.Faults(d.Slug); len(got) != 0 {
		t.Fatalf("faults outlived their deployment: %+v", got)
	}
	var none *Store
	if id, rev, err := none.AddFault("x", Fault{}); id != 0 || rev != 0 || err != nil {
		t.Fatalf("nil store: %d %d %v", id, rev, err)
	}
}

func TestRuns(t *testing.T) {
	s := open(t)
	old := Run{ID: "t01-a-1", Scenario: "t01", Name: "Baseline", Template: "A", StartedAt: 1000, EndedAt: 2000, Passed: true,
		Summary: map[string]float64{"availability": 1}, Body: []byte(`{"id":"t01-a-1","series":{}}`)}
	newer := Run{ID: "t04-b-2", Scenario: "t04", Name: "Crash", Template: "B", StartedAt: 5000, EndedAt: 6000, Aborted: true, Error: "boom", Body: []byte(`{"id":"t04-b-2"}`)}
	for _, r := range []Run{old, newer} {
		if err := s.SaveRun(r); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.Runs()
	if err != nil || len(list) != 2 || list[0].ID != newer.ID || list[1].ID != old.ID {
		t.Fatalf("Runs: %+v, %v", list, err)
	}
	if !list[1].Passed || list[1].Summary["availability"] != 1 || !list[0].Aborted || list[0].Error != "boom" || list[0].Body != nil {
		t.Fatalf("listed %+v", list)
	}
	body, ok, err := s.RunBody(old.ID)
	if err != nil || !ok || string(body) != string(old.Body) {
		t.Fatalf("RunBody: %s %v %v", body, ok, err)
	}
	old.Passed = false
	if err := s.SaveRun(old); err != nil {
		t.Fatal(err)
	}
	if list, _ = s.Runs(); len(list) != 2 || list[1].Passed {
		t.Fatalf("saving again must replace: %+v", list)
	}
	if ok, err := s.DeleteRun(old.ID); !ok || err != nil {
		t.Fatalf("DeleteRun: %v %v", ok, err)
	}
	if ok, _ := s.DeleteRun(old.ID); ok {
		t.Fatal("deleted a run twice")
	}
	if _, ok, _ := s.RunBody(old.ID); ok {
		t.Fatal("the run is still there")
	}
	var none *Store
	if err := none.SaveRun(old); err == nil {
		t.Fatal("a nil store must say it keeps nothing")
	}
	if list, err := none.Runs(); list != nil || err != nil {
		t.Fatalf("nil store: %v %v", list, err)
	}
}

// Runs an older version kept as files are taken over once.
func TestImportRuns(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "t01-x-1.json"),
		[]byte(`{"id":"t01-x-1","scenario":"t01","name":"Baseline","template":"X","started_at":1000,"ended_at":2000,"passed":true,"summary":{"availability":0.99},"series":{"error_rate":[{"t":0,"v":0}]}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "broken.json"), []byte(`{`), 0o644)
	s := open(t)
	n, err := s.ImportRuns(dir)
	if err != nil || n != 1 {
		t.Fatalf("imported %d, %v", n, err)
	}
	list, _ := s.Runs()
	if len(list) != 1 || list[0].ID != "t01-x-1" || !list[0].Passed || list[0].Summary["availability"] != 0.99 {
		t.Fatalf("imported %+v", list)
	}
	if body, _, _ := s.RunBody("t01-x-1"); !strings.Contains(string(body), `"series"`) {
		t.Fatalf("the body lost its series: %s", body)
	}
	if n, _ := s.ImportRuns(dir); n != 0 {
		t.Fatalf("second import brought %d more", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "t01-x-1.json")); err != nil {
		t.Fatal("the files must stay where they are")
	}
}
