//go:build integration

package tests_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"symphony/blocks/common/control"
	"symphony/engine/core"
	"symphony/engine/events"
	"symphony/engine/images"
	"symphony/engine/runtime"
	"symphony/engine/spec"
	"symphony/engine/store"
	"symphony/engine/tests"
)

// A run leaves the deployment as its template describes it, also when it is
// aborted halfway: the CPU limit and the work.set the scenario applied
// during its pressure phase are gone afterwards.
func TestIntegrationRunRestoresTemplate(t *testing.T) {
	ctx := context.Background()
	// an owner of its own: packages are tested in parallel, and each
	// integration test removes everything its owner label is on
	rt, err := runtime.NewDocker("symphony-test-runs")
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if _, err := rt.Ping(ctx); err != nil {
		t.Skip(err)
	}
	rt.GC(ctx)
	defer rt.GC(ctx)

	src, _ := filepath.Abs("../..")
	st, err := store.Open(filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	bus := events.NewBus()
	eng := core.New(core.Options{Runtime: rt, Bus: bus, Images: images.NewEnsurer(os.DirFS(src)), Store: st})
	runner := tests.NewRunner(eng, bus, st)

	topo := spec.Topology{Version: spec.CurrentVersion, Name: "IT run",
		Nodes: []spec.Node{
			spec.NewNode("gateway", spec.KindInfra, spec.Position{}),
			spec.NewNode("front", spec.KindService, spec.Position{}),
			spec.NewNode("back", spec.KindService, spec.Position{}),
		},
		Edges: []spec.Edge{
			{ID: "e-gw-front", From: "gateway", To: "front", Kind: spec.EdgeCalls},
			{ID: "e-front-back", From: "front", To: "back", Kind: spec.EdgeCalls},
		},
	}.Normalized()
	actx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	if err := eng.Apply(actx, topo); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// something an earlier run (or a hand) left behind, which the next run
	// must not start from
	if _, err := eng.Control(ctx, topo.Name, "back", control.Command{Op: "work.set", Params: map[string]any{"io_ms": 40.0}}); err != nil {
		t.Fatal(err)
	}
	config := func(key string) float64 {
		res, err := eng.Control(ctx, topo.Name, "back", control.Command{Op: "config.get"})
		if err != nil {
			t.Fatalf("config.get: %v", err)
		}
		v, _ := res.State.(map[string]any)[key].(float64)
		return v
	}
	limit := func() float64 {
		cur, _ := eng.Current(topo.Name)
		n, _ := cur.NodeByID("back")
		return n.Resources.CPUs
	}
	want, _ := topo.NodeByID("back")

	pressured := make(chan struct{}, 1)
	done := make(chan events.TestDonePayload, 1)
	unsub := bus.Subscribe(func(ev events.Event) {
		switch p := ev.Payload.(type) {
		case events.TestProgressPayload:
			if p.Step == "control" {
				select {
				case pressured <- struct{}{}:
				default:
				}
			}
		case events.TestDonePayload:
			done <- p
		}
	})
	defer unsub()

	id, err := runner.Start(ctx, "t10-cpu-throttle", topo, map[string]any{"rps": 10.0})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-pressured:
	case <-time.After(90 * time.Second):
		t.Fatal("the pressure phase never came")
	}
	if got := config("io_ms"); got != 0 {
		t.Errorf("the run started with io_ms %v left over from before it", got)
	}
	if config("cpu_ms") != 15 || limit() != 0.1 {
		t.Fatalf("pressure phase: cpu_ms %v, cpus %v", config("cpu_ms"), limit())
	}
	if err := runner.Abort(id); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-done:
		if !p.Aborted {
			t.Fatalf("run ended as %+v", p)
		}
	case <-time.After(restoreWait):
		t.Fatal("the aborted run never finished")
	}
	if got := config("cpu_ms"); got != 2 {
		t.Errorf("cpu_ms is %v after the run, the template says 2", got)
	}
	if got := limit(); got != want.Resources.CPUs {
		t.Errorf("cpus is %v after the run, the template says %v", got, want.Resources.CPUs)
	}
	// the run is kept in the state database, where the CLI finds it too
	runs, err := runner.ListRuns()
	if err != nil || len(runs) != 1 || runs[0].ID != id || !runs[0].Aborted {
		t.Fatalf("stored runs: %+v, %v", runs, err)
	}
	if run, err := runner.GetRun(id); err != nil || run.Scenario != "t10-cpu-throttle" || len(run.Phases) == 0 || len(run.Notes) == 0 {
		t.Fatalf("stored run: %+v, %v", run, err)
	}
	if stored, _ := st.Runs(); len(stored) != 1 {
		t.Fatalf("the store holds %d runs", len(stored))
	}
	if err := eng.Teardown(ctx, topo.Name); err != nil {
		t.Fatalf("teardown: %v", err)
	}
}

const restoreWait = 3 * time.Minute
