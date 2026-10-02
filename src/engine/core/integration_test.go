//go:build integration

// Integration test against the host's Docker daemon: applies the
// orchestration preset for real, drives load through the gateway, injects
// a link fault on the proxy, and tears everything down. Run with
// `make integration` (the builder container mounts the Docker socket and
// uses the host network so published admin ports are reachable).
package core_test

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
)

func TestIntegrationOrchestration(t *testing.T) {
	ctx := context.Background()
	rt, err := runtime.NewDocker("symphony-test")
	if err != nil {
		t.Fatal(err)
	}
	detail, err := rt.Ping(ctx)
	if err != nil {
		t.Skip(err)
	}
	defer rt.Close()
	t.Log("docker", detail)
	if n, err := rt.GC(ctx); err == nil && n > 0 {
		t.Logf("removed %d leftovers from a previous run", n)
	}
	defer func() {
		n, _ := rt.GC(ctx)
		t.Logf("gc removed %d objects", n)
	}()

	src, _ := filepath.Abs("../..")
	bus := events.NewBus()
	var logs []string
	bus.Subscribe(func(ev events.Event) {
		if l, ok := ev.Payload.(events.LogPayload); ok {
			logs = append(logs, l.Level+" "+l.Source+" "+l.Message)
			if testing.Verbose() {
				t.Log(l.Level, l.Source, l.Message)
			}
		}
	})
	db, err := store.Open(filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	eng := core.New(core.Options{Runtime: rt, Bus: bus, Images: images.NewEnsurer(os.DirFS(src)), Store: db})
	eng.Start(ctx)
	defer eng.Stop()

	topo := spec.Presets()[0] // Orchestration (saga)
	topo.Name = "IT orchestration"
	actx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	if err := eng.Apply(actx, topo); err != nil {
		for _, l := range logs {
			t.Log(l)
		}
		t.Fatalf("apply: %v", err)
	}
	deps := eng.Deployments()
	if len(deps) != 1 || deps[0].Phase != core.PhaseRunning {
		t.Fatalf("expected one running deployment, got %+v", deps)
	}
	for id, st := range deps[0].Statuses {
		if st.State != "healthy" {
			t.Errorf("node %s is %s: %s", id, st.State, st.Message)
		}
		if len(st.Checks) == 0 {
			t.Errorf("node %s has no deep health checks", id)
		}
	}

	// load through the real gateway
	if _, err := eng.Control(ctx, topo.Name, "gateway", control.Command{Op: "load.set", Params: map[string]any{"rps": 30, "concurrency": 32}, DurationMs: 4000}); err != nil {
		t.Fatalf("load.set: %v", err)
	}
	time.Sleep(5 * time.Second)
	res, err := eng.Control(ctx, topo.Name, "gateway", control.Command{Op: "sagas.summary"})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	st := res.State.(map[string]any)
	by, _ := st["by_outcome"].(map[string]any)
	okc, _ := by["ok"].(float64)
	if okc < 60 {
		t.Fatalf("expected >= 60 ok sagas through real containers, got %v (%v)", okc, st)
	}
	t.Logf("sagas: %v", st)

	// hot config for real
	if err := eng.SetHotConfig(ctx, topo.Name, "orchestrator", map[string]any{"timeout_ms": 1500}); err != nil {
		t.Fatalf("hot config: %v", err)
	}
	cfg, _ := eng.Control(ctx, topo.Name, "orchestrator", control.Command{Op: "config.get"})
	if v := cfg.State.(map[string]any)["timeout_ms"]; v != 1500.0 {
		t.Fatalf("timeout_ms not applied: %v", v)
	}

	// a link fault on the proxy, then a saga must compensate
	if err := eng.InjectFault(ctx, topo.Name, "toxiproxy", spec.FaultSpec{Kind: spec.FaultDrop, DurationMs: 8000, Params: map[string]any{"rate": 1.0, "edge": "e-proxy-pay", "direction": "out"}}); err != nil {
		t.Fatalf("inject: %v", err)
	}
	if _, err := eng.Control(ctx, topo.Name, "gateway", control.Command{Op: "load.set", Params: map[string]any{"rps": 10, "concurrency": 8}, DurationMs: 2000}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	res, _ = eng.Control(ctx, topo.Name, "gateway", control.Command{Op: "sagas.summary"})
	by, _ = res.State.(map[string]any)["by_outcome"].(map[string]any)
	if comp, _ := by["compensated"].(float64); comp < 5 {
		recent, _ := eng.Control(ctx, topo.Name, "gateway", control.Command{Op: "sagas.recent", Params: map[string]any{"n": 5}})
		t.Fatalf("expected compensated sagas while the proxied link drops, got %v; recent: %v", by, recent.State)
	}
	if err := eng.ClearFaults(ctx, topo.Name, "toxiproxy"); err != nil {
		t.Fatal(err)
	}

	// resources live
	if err := eng.SetResources(ctx, topo.Name, "payment", spec.Resources{CPUs: 0.25, MemoryMB: 128}); err != nil {
		t.Fatalf("resources: %v", err)
	}
	// container-level fault: pause + unpause
	if err := eng.InjectFault(ctx, topo.Name, "inventory", spec.FaultSpec{Kind: spec.FaultPause, DurationMs: 1500, Params: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	c, _ := rt.Inspect(ctx, "it-orchestration-inventory")
	if c.State != runtime.StatePaused {
		t.Fatalf("inventory should be paused, is %s", c.State)
	}
	time.Sleep(2 * time.Second)

	// logs come from the block
	lines, err := eng.Logs(ctx, topo.Name, "gateway", 20)
	if err != nil || len(lines) == 0 {
		t.Fatalf("logs: %v %d", err, len(lines))
	}

	if err := eng.Teardown(ctx, topo.Name); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if left, _ := rt.List(ctx); len(left) != 0 {
		t.Fatalf("containers left after teardown: %v", left)
	}
}

// A closed Symphony comes back to what the store says: running containers
// are attached as they are, and a deployment whose containers are gone is
// recreated with the load its gateway was driving.
func TestIntegrationReopen(t *testing.T) {
	ctx := context.Background()
	rt, err := runtime.NewDocker("symphony-test")
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
	session := func() *core.Engine {
		return core.New(core.Options{Runtime: rt, Bus: events.NewBus(), Images: images.NewEnsurer(os.DirFS(src)), Store: st})
	}

	topo := spec.Presets()[1] // Choreography (saga)
	topo.Name = "IT reopen"
	actx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	first := session()
	if err := first.Apply(actx, topo); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := first.Control(ctx, topo.Name, "gateway", control.Command{Op: "load.set", Params: map[string]any{"rps": 5.0}}); err != nil {
		t.Fatalf("load.set: %v", err)
	}

	// the app closes and opens again with everything still running
	second := session()
	broken, err := second.Attach(ctx)
	if err != nil || len(broken) != 0 {
		t.Fatalf("attach with everything running: broken %v, %v", broken, err)
	}
	if deps := second.Deployments(); len(deps) != 1 || deps[0].Phase != core.PhaseRunning {
		t.Fatalf("attached %+v", deps)
	}

	// Docker loses the containers while the app is closed
	list, _ := rt.List(ctx)
	for _, c := range list {
		if err := rt.Destroy(ctx, c.Name); err != nil {
			t.Fatal(err)
		}
	}
	third := session()
	broken, err = third.Attach(ctx)
	if err != nil || len(broken) != 1 || broken[0] != topo.Name {
		t.Fatalf("attach without containers: broken %v, %v", broken, err)
	}
	if err := third.Reopen(actx, topo.Name); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	deps := third.Deployments()
	if len(deps) != 1 || deps[0].Phase != core.PhaseRunning {
		t.Fatalf("after reopen: %+v", deps)
	}
	for id, s := range deps[0].Statuses {
		if s.State != "healthy" {
			t.Errorf("node %s is %s after reopen", id, s.State)
		}
	}
	res, err := third.Control(ctx, topo.Name, "gateway", control.Command{Op: "load.status"})
	if err != nil {
		t.Fatal(err)
	}
	if running, _ := res.State.(map[string]any)["running"].(bool); !running {
		t.Fatalf("the load was not restarted: %v", res.State)
	}

	if err := third.Teardown(ctx, topo.Name); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if left, _ := st.Deployments(); len(left) != 0 {
		t.Fatalf("still stored after teardown: %+v", left)
	}
}

// Faults are undone completely, and by whichever Symphony is there when
// they are due: a partitioned node answers on its admin port again, a
// stored fault survives the session that injected it, and one that became
// overdue while nothing was running is ended by the next Restore.
func TestIntegrationFaults(t *testing.T) {
	ctx := context.Background()
	rt, err := runtime.NewDocker("symphony-test")
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
	session := func() *core.Engine {
		return core.New(core.Options{Runtime: rt, Bus: events.NewBus(), Images: images.NewEnsurer(os.DirFS(src)), Store: st})
	}

	topo := spec.Topology{Version: spec.CurrentVersion, Name: "IT faults",
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
	slug := spec.Slug(topo.Name)
	back := slug + "-back"
	actx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	first := session()
	if err := first.Apply(actx, topo); err != nil {
		t.Fatalf("apply: %v", err)
	}
	answers := func(eng *core.Engine, node string) error {
		var err error
		for i := 0; i < 50; i++ {
			if _, err = eng.Control(ctx, topo.Name, node, control.Command{Op: "config.get"}); err == nil {
				return nil
			}
			time.Sleep(200 * time.Millisecond)
		}
		return err
	}

	// a whole-node partition, cleared: the admin port is reachable again
	if err := first.InjectFault(ctx, topo.Name, "back", spec.FaultSpec{Kind: spec.FaultPartition}); err != nil {
		t.Fatalf("partition: %v", err)
	}
	if fs, _ := st.Faults(slug); len(fs) != 1 || fs[0].Kind != "partition" {
		t.Fatalf("stored faults after partition: %+v", fs)
	}
	if err := first.ClearFaults(ctx, topo.Name, "back"); err != nil {
		t.Fatal(err)
	}
	if err := answers(first, "back"); err != nil {
		t.Fatalf("back does not answer after the partition was cleared: %v", err)
	}
	if fs, _ := st.Faults(slug); len(fs) != 0 {
		t.Fatalf("faults still stored after clearing: %+v", fs)
	}

	// held memory and a crash are both undone by clearing
	if err := first.InjectFault(ctx, topo.Name, "back", spec.FaultSpec{Kind: spec.FaultMemory, Params: map[string]any{"mb": 20.0}}); err != nil {
		t.Fatalf("memory: %v", err)
	}
	if err := first.ClearFaults(ctx, topo.Name, "back"); err != nil {
		t.Fatal(err)
	}
	snap, err := first.Control(ctx, topo.Name, "back", control.Command{Op: "chaos.clear"})
	if err != nil {
		t.Fatal(err)
	}
	if left, _ := snap.State.(map[string]any)["released_mb"].(float64); left != 0 {
		t.Fatalf("memory was still held after clearing: %v", snap.State)
	}
	if err := first.InjectFault(ctx, topo.Name, "back", spec.FaultSpec{Kind: spec.FaultCrash}); err != nil {
		t.Fatalf("crash: %v", err)
	}
	time.Sleep(time.Second)
	if c, _ := rt.Inspect(ctx, back); c.Running {
		t.Fatalf("back still runs after a crash")
	}
	if err := first.ClearFaults(ctx, topo.Name, "back"); err != nil {
		t.Fatal(err)
	}
	if err := answers(first, "back"); err != nil {
		t.Fatalf("back does not answer after the crash was cleared: %v", err)
	}

	// a kill another session injected, not due yet: the next session leaves
	// the node down instead of recreating the deployment
	if err := rt.Kill(ctx, back); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.AddFault(slug, store.Fault{Node: "back", Kind: "kill", Until: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	// ...and a link fault that ended inside its block while nobody was
	// there to drop the record: any session that attaches forgets it
	if _, _, err := st.AddFault(slug, store.Fault{Node: "front", Kind: "latency", BlockID: "f-9", Until: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	second := session()
	broken, err := second.Attach(ctx)
	if err != nil || len(broken) != 0 {
		t.Fatalf("attach with a node held down by a fault: broken %v, %v", broken, err)
	}
	if fs, _ := st.Faults(slug); len(fs) != 1 || fs[0].Kind != "kill" {
		t.Fatalf("stored faults after attach: %+v", fs)
	}
	if err := second.ClearFaults(ctx, topo.Name, "back"); err != nil {
		t.Fatal(err)
	}
	if err := answers(second, "back"); err != nil {
		t.Fatalf("back does not answer after the kill was cleared: %v", err)
	}

	// a pause that became overdue while nothing was running
	if err := rt.Pause(ctx, back); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.AddFault(slug, store.Fault{Node: "back", Kind: "pause", Until: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	third := session()
	third.Restore(ctx)
	if c, _ := rt.Inspect(ctx, back); c.State == runtime.StatePaused {
		t.Fatalf("back still paused after Restore")
	}
	if fs, _ := st.Faults(slug); len(fs) != 0 {
		t.Fatalf("overdue fault still stored: %+v", fs)
	}
	if deps := third.Deployments(); len(deps) != 1 || deps[0].Phase != core.PhaseRunning {
		t.Fatalf("after restore: %+v", deps)
	}

	if err := third.Teardown(ctx, topo.Name); err != nil {
		t.Fatalf("teardown: %v", err)
	}
}

// An apply that fails halfway leaves containers behind that the applied
// topology never included. Applying again replaces them instead of tripping
// over their names, and a teardown removes them too.
func TestIntegrationRetryAfterFailedApply(t *testing.T) {
	ctx := context.Background()
	rt, err := runtime.NewDocker("symphony-test")
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
	eng := core.New(core.Options{Runtime: rt, Bus: events.NewBus(), Images: images.NewEnsurer(os.DirFS(src)), Store: st})

	good := spec.Topology{Version: spec.CurrentVersion, Name: "IT retry",
		Nodes: []spec.Node{
			spec.NewNode("gateway", spec.KindInfra, spec.Position{}),
			spec.NewNode("front", spec.KindService, spec.Position{}),
		},
		Edges: []spec.Edge{{ID: "e-gw-front", From: "gateway", To: "front", Kind: spec.EdgeCalls}},
	}.Normalized()
	// the service node runs the database image, which refuses to start as a
	// service: the gateway is created first and then the apply fails
	bad := good.Clone()
	bad.Nodes[1].Image = spec.DefaultImage(spec.KindDB)

	actx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	if err := eng.Apply(actx, bad); err == nil {
		t.Fatal("the broken topology applied")
	}
	if left, _ := rt.List(ctx); len(left) == 0 {
		t.Fatal("the failed apply left nothing behind; the test does not test what it means to")
	}
	if err := eng.Apply(actx, good); err != nil {
		t.Fatalf("applying again after a failed apply: %v", err)
	}
	if deps := eng.Deployments(); len(deps) != 1 || deps[0].Phase != core.PhaseRunning {
		t.Fatalf("after the retry: %+v", deps)
	}
	if err := eng.Teardown(ctx, good.Name); err != nil {
		t.Fatalf("teardown: %v", err)
	}

	// a failed apply, torn down without a retry
	if err := eng.Apply(actx, bad); err == nil {
		t.Fatal("the broken topology applied")
	}
	if err := eng.Teardown(ctx, bad.Name); err != nil {
		t.Fatalf("teardown after a failed apply: %v", err)
	}
	if left, _ := rt.List(ctx); len(left) != 0 {
		t.Fatalf("containers left after teardown: %v", left)
	}
	if recs, _ := st.Deployments(); len(recs) != 0 {
		t.Fatalf("still stored: %+v", recs)
	}
}

// The window's engine follows the store and polls while it applies. A
// deployment it applied itself must be usable the moment Apply returns, and
// stay so: its nodes ready and their admin ports reachable.
func TestIntegrationFollowingEngineApplies(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	rt, err := runtime.NewDocker("symphony-test")
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if _, err := rt.Ping(ctx); err != nil {
		t.Skip(err)
	}
	rt.GC(ctx)
	defer rt.GC(context.Background())

	src, _ := filepath.Abs("../..")
	st, err := store.Open(filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	bus := events.NewBus()
	bus.Subscribe(func(ev events.Event) {
		if l, ok := ev.Payload.(events.LogPayload); ok && testing.Verbose() {
			t.Log(l.Level, l.Source, l.Message)
		}
	})
	eng := core.New(core.Options{Runtime: rt, Bus: bus, Images: images.NewEnsurer(os.DirFS(src)), Store: st, FollowStore: true})
	eng.Restore(ctx)
	eng.Start(ctx)
	defer eng.Stop()

	topo := spec.Presets()[0]
	topo.Name = "IT follow"
	actx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	if err := eng.Apply(actx, topo); err != nil {
		t.Fatalf("apply: %v", err)
	}
	check := func(when string) {
		t.Helper()
		if _, err := eng.Control(ctx, topo.Name, "gateway", control.Command{Op: "load.status"}); err != nil {
			t.Fatalf("%s: gateway: %v", when, err)
		}
		for id, s := range eng.Deployments()[0].Statuses {
			if s.Ready != s.Replicas {
				t.Errorf("%s: %s has %d/%d replicas ready (%s)", when, id, s.Ready, s.Replicas, s.State)
			}
		}
	}
	check("right after apply")
	time.Sleep(8 * time.Second) // a few rounds of the poller and the store follower
	check("a while after apply")
	if err := eng.Teardown(ctx, topo.Name); err != nil {
		t.Fatalf("teardown: %v", err)
	}
}
