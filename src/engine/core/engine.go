// Package engine drives a topology from desired state to running state:
// validate -> diff -> plan -> execute against a Runtime, emitting progress
// on the event bus. It also owns per-node status and the hot-config /
// fault-injection entry points used by the UI.
package core

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"symphony/engine/events"
	"symphony/engine/planner"
	"symphony/engine/runtime"
	"symphony/engine/spec"
)

// Phase is the engine's coarse state, shown in the toolbar pill.
type Phase string

const (
	PhaseIdle        Phase = "idle"
	PhaseValidating  Phase = "validating"
	PhasePlanning    Phase = "planning"
	PhaseApplying    Phase = "applying"
	PhaseRunning     Phase = "running"
	PhaseTearingDown Phase = "tearing_down"
	PhaseFailed      Phase = "failed"
)

// NodeStatus is the aggregated runtime state of one topology node across
// its replicas. State is the "worst" replica state.
type NodeStatus struct {
	NodeID    string `json:"node_id"`
	State     string `json:"state"` // pending | starting | healthy | failed | stopped | paused
	Replicas  int    `json:"replicas"`
	Ready     int    `json:"ready"`
	Message   string `json:"message,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
}

var (
	ErrBusy           = errors.New("engine: an apply is already in progress")
	ErrNodeNotRunning = errors.New("engine: node is not running; apply the topology first")
	ErrUnknownHotKey  = errors.New("engine: unknown hot config key")
	ErrValidation     = errors.New("engine: topology failed validation")
)

const (
	healthyTimeout = 15 * time.Second
	healthyPoll    = 60 * time.Millisecond
)

// Engine is safe for concurrent use; Wails invokes bound methods from
// multiple goroutines.
type Engine struct {
	rt  runtime.Runtime
	bus *events.Bus

	mu         sync.Mutex
	phase      Phase
	current    spec.Topology
	statuses   map[string]*NodeStatus
	containers map[string]string // container name -> node id
	faults     map[string][]spec.FaultSpec
	applying   bool
}

func New(rt runtime.Runtime, bus *events.Bus) *Engine {
	e := &Engine{
		rt:         rt,
		bus:        bus,
		phase:      PhaseIdle,
		current:    spec.Topology{Version: spec.CurrentVersion, Name: "empty"}.Normalized(),
		statuses:   map[string]*NodeStatus{},
		containers: map[string]string{},
		faults:     map[string][]spec.FaultSpec{},
	}
	if l, ok := rt.(runtime.StateListener); ok {
		l.OnStateChange(e.onContainerState)
	}
	return e
}

// --- read side ------------------------------------------------------------

func (e *Engine) Current() spec.Topology {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.current.Clone()
}

func (e *Engine) Phase() Phase {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.phase
}

func (e *Engine) NodeStatuses() map[string]NodeStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]NodeStatus, len(e.statuses))
	for k, v := range e.statuses {
		out[k] = *v
	}
	return out
}

// MetricInputs feeds the mock metrics ticker so its numbers react to what
// the engine is doing.
type MetricInputs struct {
	Running      bool
	Total        int
	Healthy      int
	ActiveFaults int
}

func (e *Engine) MetricInputs() MetricInputs {
	e.mu.Lock()
	defer e.mu.Unlock()
	in := MetricInputs{Running: e.phase == PhaseRunning || e.phase == PhaseApplying}
	for _, s := range e.statuses {
		in.Total++
		if s.State == string(runtime.StateHealthy) {
			in.Healthy++
		}
	}
	for _, fs := range e.faults {
		in.ActiveFaults += len(fs)
	}
	return in
}

// --- plan / apply ---------------------------------------------------------

// Plan validates and diffs without executing. Validation findings are
// returned alongside the plan; the plan is empty when there are errors.
func (e *Engine) Plan(desired spec.Topology) (planner.Plan, []spec.ValidationError) {
	desired = desired.Normalized()
	findings := desired.Validate()
	if spec.HasErrors(findings) {
		return planner.Plan{Ops: []planner.Operation{}}, findings
	}
	return planner.Diff(e.Current(), desired), findings
}

// Apply runs the full pipeline. It blocks until the plan has executed and
// streams apply:progress / node:status / engine:log events meanwhile.
func (e *Engine) Apply(ctx context.Context, desired spec.Topology) error {
	if err := e.begin(); err != nil {
		return err
	}
	defer e.end()

	desired = desired.Normalized()
	e.setPhase(PhaseValidating, "")
	findings := desired.Validate()
	if spec.HasErrors(findings) {
		msgs := []string{}
		for _, f := range findings {
			if f.Severity == spec.SeverityError {
				msgs = append(msgs, f.Error())
			}
		}
		e.log("error", "engine", "validation failed: "+strings.Join(msgs, "; "))
		e.setPhase(PhaseFailed, "validation failed")
		return fmt.Errorf("%w: %s", ErrValidation, strings.Join(msgs, "; "))
	}
	for _, f := range findings {
		e.log("warn", "validate", f.Error())
	}

	e.setPhase(PhasePlanning, "")
	plan := planner.Diff(e.Current(), desired)
	e.log("info", "planner", fmt.Sprintf("plan for %q: %d ops (%d hot, %d cold, %d structural)",
		desired.Name, len(plan.Ops), plan.Summary.Hot, plan.Summary.Cold, plan.Summary.Structural))

	e.setPhase(PhaseApplying, "")
	start := time.Now()
	if err := e.execute(ctx, plan, desired); err != nil {
		e.setPhase(PhaseFailed, err.Error())
		return err
	}

	e.mu.Lock()
	e.current = desired
	e.mu.Unlock()
	e.log("info", "engine", fmt.Sprintf("apply complete in %s", time.Since(start).Round(time.Millisecond)))
	if len(desired.Nodes) == 0 {
		e.setPhase(PhaseIdle, "")
	} else {
		e.setPhase(PhaseRunning, "")
	}
	return nil
}

// Teardown destroys everything that is running.
func (e *Engine) Teardown(ctx context.Context) error {
	if err := e.begin(); err != nil {
		return err
	}
	defer e.end()

	cur := e.Current()
	empty := spec.Topology{Version: cur.Version, Name: cur.Name}.Normalized()
	plan := planner.Diff(cur, empty)
	e.setPhase(PhaseTearingDown, "")
	e.log("info", "engine", fmt.Sprintf("teardown: %d ops", len(plan.Ops)))
	if err := e.execute(ctx, plan, empty); err != nil {
		e.setPhase(PhaseFailed, err.Error())
		return err
	}
	e.mu.Lock()
	e.current = empty
	e.faults = map[string][]spec.FaultSpec{}
	e.mu.Unlock()
	e.setPhase(PhaseIdle, "")
	return nil
}

func (e *Engine) execute(ctx context.Context, plan planner.Plan, desired spec.Topology) error {
	for i, op := range plan.Ops {
		started := time.Now()
		e.progress(op, i, len(plan.Ops), "started", "", 0)
		err := e.executeOp(ctx, op, plan.Network, desired)
		elapsed := time.Since(started).Milliseconds()
		if err != nil {
			e.progress(op, i, len(plan.Ops), "failed", err.Error(), elapsed)
			e.log("error", "engine", fmt.Sprintf("%s %s failed: %v", op.Kind, op.Target, err))
			return fmt.Errorf("%s %s: %w", op.Kind, op.Target, err)
		}
		e.progress(op, i, len(plan.Ops), "done", "", elapsed)
	}
	return nil
}

func (e *Engine) executeOp(ctx context.Context, op planner.Operation, network string, desired spec.Topology) error {
	switch op.Kind {
	case planner.OpEnsureNetwork:
		return e.rt.EnsureNetwork(ctx, op.Target)

	case planner.OpCreate:
		node, ok := desired.NodeByID(op.Target)
		if !ok {
			return fmt.Errorf("node %q not in desired topology", op.Target)
		}
		e.setStatus(node.ID, string(runtime.StatePending), node.Replicas, 0, "")
		for r := 1; r <= node.Replicas; r++ {
			name := containerName(node.ID, r, node.Replicas)
			cspec := runtime.ContainerSpec{
				Name:    name,
				Image:   node.Image,
				Network: network,
				Env:     envFor(node),
				Labels: map[string]string{
					"symphony.node":     node.ID,
					"symphony.kind":     string(node.Kind),
					"symphony.topology": desired.Name,
				},
			}
			e.mu.Lock()
			e.containers[name] = node.ID
			e.mu.Unlock()
			if _, err := e.rt.Create(ctx, cspec); err != nil {
				return err
			}
			e.log("info", "runtime", fmt.Sprintf("created %s (%s)", name, node.Image))
			if err := e.rt.Start(ctx, name); err != nil {
				return err
			}
		}
		return nil

	case planner.OpWaitHealthy:
		return e.waitHealthy(ctx, op.Target)

	case planner.OpEnsureTopic:
		// Seam: a real implementation talks to the broker's admin API
		// (e.g. kafka CreateTopics) using op.Params["broker"] / ["topic"].
		if err := sleepCtx(ctx, 80*time.Millisecond); err != nil {
			return err
		}
		e.log("info", "broker", fmt.Sprintf("ensured topic %v on %v", op.Params["topic"], op.Params["broker"]))
		return nil

	case planner.OpControl:
		// Seam: push the patch to the running container (HTTP admin endpoint,
		// SIGHUP + config file, or toxiproxy API for proxy nodes).
		if err := sleepCtx(ctx, 40*time.Millisecond); err != nil {
			return err
		}
		e.log("info", "control", fmt.Sprintf("%s: %s", op.Target, op.Reason))
		return nil

	case planner.OpDestroy:
		names := e.containersFor(op.Target)
		for _, name := range names {
			if err := e.rt.Destroy(ctx, name); err != nil && !errors.Is(err, runtime.ErrNotFound) {
				return err
			}
			e.mu.Lock()
			delete(e.containers, name)
			e.mu.Unlock()
			e.log("info", "runtime", "destroyed "+name)
		}
		e.mu.Lock()
		delete(e.statuses, op.Target)
		delete(e.faults, op.Target)
		e.mu.Unlock()
		e.bus.Publish(events.NodeStatus, events.NodeStatusPayload{
			NodeID: op.Target, State: "removed", UpdatedAt: time.Now().UnixMilli(),
		})
		return nil
	}
	return fmt.Errorf("unknown op kind %q", op.Kind)
}

func (e *Engine) waitHealthy(ctx context.Context, nodeID string) error {
	deadline := time.Now().Add(healthyTimeout)
	for {
		names := e.containersFor(nodeID)
		healthy := 0
		for _, name := range names {
			c, err := e.rt.Inspect(ctx, name)
			if err != nil {
				return err
			}
			switch c.State {
			case runtime.StateHealthy:
				healthy++
			case runtime.StateFailed:
				return fmt.Errorf("container %s failed to start", name)
			}
		}
		if len(names) > 0 && healthy == len(names) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s (%d/%d healthy)", nodeID, healthy, len(names))
		}
		if err := sleepCtx(ctx, healthyPoll); err != nil {
			return err
		}
	}
}

// --- hot config / faults --------------------------------------------------

// SetHotConfig applies a hot patch to a running node immediately. Keys must
// already exist in the node's hot config; the patch is merged into the
// engine's current topology so a later Diff sees no drift.
func (e *Engine) SetHotConfig(ctx context.Context, nodeID string, patch map[string]any) error {
	e.mu.Lock()
	idx := -1
	for i, n := range e.current.Nodes {
		if n.ID == nodeID {
			idx = i
		}
	}
	if idx < 0 || e.statuses[nodeID] == nil {
		e.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrNodeNotRunning, nodeID)
	}
	node := &e.current.Nodes[idx]
	changed := map[string]any{}
	for k, v := range patch {
		if _, ok := node.Config.Hot[k]; !ok {
			e.mu.Unlock()
			return fmt.Errorf("%w: %s.%s", ErrUnknownHotKey, nodeID, k)
		}
		if spec.CanonValue(node.Config.Hot[k]) != spec.CanonValue(v) {
			node.Config.Hot[k] = v
			changed[k] = v
		}
	}
	e.mu.Unlock()
	if len(changed) == 0 {
		return nil
	}

	op := planner.Operation{
		ID:     "control-" + nodeID,
		Kind:   planner.OpControl,
		Target: nodeID,
		Class:  planner.ClassHot,
		Params: changed,
	}
	e.progress(op, 0, 1, "started", "", 0)
	// Seam: same control transport as executeOp(OpControl).
	e.progress(op, 0, 1, "done", "", 0)
	parts := []string{}
	for k, v := range changed {
		parts = append(parts, fmt.Sprintf("%s=%s", k, spec.CanonValue(v)))
	}
	sort.Strings(parts)
	e.log("info", "control", fmt.Sprintf("%s: %s (no restart)", nodeID, strings.Join(parts, ", ")))
	return nil
}

// InjectFault records a fault against a running node. Kill and pause are
// executed through the runtime because they are pure container operations;
// latency, drop, partition and cpu_stress are logged only until the proxy /
// tc / stress seams exist.
func (e *Engine) InjectFault(ctx context.Context, nodeID string, fault spec.FaultSpec) error {
	if !fault.Kind.Valid() {
		return fmt.Errorf("engine: unknown fault kind %q", fault.Kind)
	}
	e.mu.Lock()
	st := e.statuses[nodeID]
	if st == nil {
		e.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrNodeNotRunning, nodeID)
	}
	e.faults[nodeID] = append(e.faults[nodeID], fault)
	e.mu.Unlock()

	names := e.containersFor(nodeID)
	switch fault.Kind {
	case spec.FaultPause:
		for _, n := range names {
			if err := e.rt.Pause(ctx, n); err != nil {
				return err
			}
		}
	case spec.FaultKill:
		for _, n := range names {
			if err := e.rt.Stop(ctx, n); err != nil {
				return err
			}
		}
	default:
		// Seam: toxiproxy toxics for latency/drop, netem/iptables for
		// partition, stress-ng exec for cpu_stress.
	}
	e.log("warn", "fault", fmt.Sprintf("injected %s into %s (%dms) %v", fault.Kind, nodeID, fault.DurationMs, fault.Params))
	e.bus.Publish(events.FaultInjected, events.FaultPayload{NodeID: nodeID, Kind: string(fault.Kind), DurationMs: fault.DurationMs, Active: true})
	e.setStatusMessage(nodeID, "fault: "+string(fault.Kind))

	if fault.DurationMs > 0 {
		time.AfterFunc(time.Duration(fault.DurationMs)*time.Millisecond, func() {
			e.clearFault(nodeID, fault)
		})
	}
	return nil
}

func (e *Engine) clearFault(nodeID string, fault spec.FaultSpec) {
	e.mu.Lock()
	fs := e.faults[nodeID]
	for i, f := range fs {
		if f.Kind == fault.Kind {
			e.faults[nodeID] = append(fs[:i], fs[i+1:]...)
			break
		}
	}
	e.mu.Unlock()
	ctx := context.Background()
	for _, n := range e.containersFor(nodeID) {
		switch fault.Kind {
		case spec.FaultPause:
			_ = e.rt.Unpause(ctx, n)
		case spec.FaultKill:
			_ = e.rt.Start(ctx, n)
		}
	}
	e.log("info", "fault", fmt.Sprintf("cleared %s on %s", fault.Kind, nodeID))
	e.bus.Publish(events.FaultInjected, events.FaultPayload{NodeID: nodeID, Kind: string(fault.Kind), Active: false})
	e.setStatusMessage(nodeID, "")
}

// --- status bookkeeping ---------------------------------------------------

func (e *Engine) onContainerState(c runtime.Container) {
	e.mu.Lock()
	nodeID, ok := e.containers[c.Name]
	if !ok {
		e.mu.Unlock()
		return
	}
	e.mu.Unlock()
	e.recomputeStatus(nodeID)
}

func (e *Engine) recomputeStatus(nodeID string) {
	names := e.containersFor(nodeID)
	ctx := context.Background()
	ready := 0
	worst := runtime.StateHealthy
	for _, n := range names {
		c, err := e.rt.Inspect(ctx, n)
		if err != nil {
			continue
		}
		if c.State == runtime.StateHealthy {
			ready++
		}
		if stateRank(c.State) > stateRank(worst) {
			worst = c.State
		}
	}
	e.mu.Lock()
	st := e.statuses[nodeID]
	if st == nil {
		e.mu.Unlock()
		return
	}
	replicas := st.Replicas
	msg := st.Message
	e.mu.Unlock()
	e.setStatus(nodeID, string(worst), replicas, ready, msg)
}

// stateRank orders states by "badness" so a node reports its worst replica.
func stateRank(s runtime.State) int {
	switch s {
	case runtime.StateHealthy:
		return 0
	case runtime.StateStarting:
		return 1
	case runtime.StatePending:
		return 2
	case runtime.StatePaused:
		return 3
	case runtime.StateStopped:
		return 4
	case runtime.StateFailed:
		return 5
	}
	return 6
}

func (e *Engine) setStatus(nodeID, state string, replicas, ready int, msg string) {
	e.mu.Lock()
	st := &NodeStatus{NodeID: nodeID, State: state, Replicas: replicas, Ready: ready, Message: msg, UpdatedAt: time.Now().UnixMilli()}
	e.statuses[nodeID] = st
	payload := events.NodeStatusPayload(*st)
	e.mu.Unlock()
	e.bus.Publish(events.NodeStatus, payload)
}

func (e *Engine) setStatusMessage(nodeID, msg string) {
	e.mu.Lock()
	st := e.statuses[nodeID]
	if st == nil {
		e.mu.Unlock()
		return
	}
	st.Message = msg
	st.UpdatedAt = time.Now().UnixMilli()
	payload := events.NodeStatusPayload(*st)
	e.mu.Unlock()
	e.bus.Publish(events.NodeStatus, payload)
}

func (e *Engine) containersFor(nodeID string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for name, id := range e.containers {
		if id == nodeID {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func (e *Engine) begin() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.applying {
		return ErrBusy
	}
	e.applying = true
	return nil
}

func (e *Engine) end() {
	e.mu.Lock()
	e.applying = false
	e.mu.Unlock()
}

func (e *Engine) setPhase(p Phase, msg string) {
	e.mu.Lock()
	e.phase = p
	e.mu.Unlock()
	e.bus.Publish(events.EnginePhase, events.PhasePayload{Phase: string(p), Message: msg})
}

func (e *Engine) progress(op planner.Operation, idx, count int, status, msg string, elapsed int64) {
	e.bus.Publish(events.ApplyProgress, events.ProgressPayload{
		OpID: op.ID, Index: idx, Count: count, Kind: string(op.Kind), Target: op.Target,
		Class: string(op.Class), Status: status, Message: msg, Elapsed: elapsed,
	})
}

func (e *Engine) log(level, source, msg string) {
	e.bus.Log(level, source, msg)
}

func containerName(nodeID string, replica, total int) string {
	if total <= 1 {
		return nodeID
	}
	return fmt.Sprintf("%s-%d", nodeID, replica)
}

// envFor flattens cold config into environment variables; hot config is
// delivered through the control channel instead.
func envFor(n spec.Node) map[string]string {
	env := map[string]string{"SYMPHONY_NODE": n.ID, "SYMPHONY_KIND": string(n.Kind)}
	for k, v := range n.Config.Cold {
		env["CFG_"+strings.ToUpper(k)] = fmt.Sprintf("%v", v)
	}
	return env
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
