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
	dir := t.TempDir()
	eng := core.New(core.Options{Runtime: rt, Bus: bus, Images: images.NewEnsurer(os.DirFS(src)), Store: core.NewStore(dir)})
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
