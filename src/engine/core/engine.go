// Package core drives topologies from desired state to running state:
// validate -> diff -> plan -> execute against a Runtime, emitting progress
// on the event bus. Several topologies can be applied at once; each one is a
// deployment with its own network, containers, statuses and faults, keyed by
// the topology's name. The engine also owns the hot-config and
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

// Phase is a deployment's coarse state, shown in the toolbar pill.
type Phase string

const (
	PhaseIdle        Phase = "idle"
	PhaseValidating  Phase = "validating"
	PhasePlanning    Phase = "planning"
	PhaseApplying    Phase = "applying"
	PhaseRunning     Phase = "running"
	PhaseTearingDown Phase = "tearing_down"
	PhaseFailed      Phase = "failed"
	// PhaseGone is only ever emitted: the deployment was torn down.
	PhaseGone Phase = "gone"
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

// DeploymentInfo is the read-side snapshot of one applied topology.
type DeploymentInfo struct {
	Name     string                `json:"name"`
	Phase    Phase                 `json:"phase"`
	Network  string                `json:"network"`
	Topology spec.Topology         `json:"topology"`
	Statuses map[string]NodeStatus `json:"statuses"`
}

var (
	ErrBusy           = errors.New("engine: an apply is already in progress for this topology")
	ErrNotDeployed    = errors.New("engine: topology is not applied")
	ErrNodeNotRunning = errors.New("engine: node is not running; apply the topology first")
	ErrUnknownHotKey  = errors.New("engine: unknown hot config key")
	ErrValidation     = errors.New("engine: topology failed validation")
)

const (
	healthyTimeout = 15 * time.Second
	healthyPoll    = 60 * time.Millisecond
)

// deployment is everything the engine tracks for one applied topology.
// Container names are prefixed with the slug so two deployments that share
// node ids (every preset has a "gateway") never collide in the runtime.
type deployment struct {
	name     string
	slug     string
	network  string
	phase    Phase
	current  spec.Topology
	statuses map[string]*NodeStatus
	faults   map[string][]spec.FaultSpec
	applying bool
}

// containerRef says which deployment and node a runtime container belongs to.
type containerRef struct {
	dep  string
	node string
}

// Engine is safe for concurrent use; Wails invokes bound methods from
// multiple goroutines, and different deployments may apply at the same time.
type Engine struct {
	rt  runtime.Runtime
	bus *events.Bus

	mu         sync.Mutex
	deps       map[string]*deployment
	containers map[string]containerRef // container name -> owner
}

func New(rt runtime.Runtime, bus *events.Bus) *Engine {
	e := &Engine{
		rt:         rt,
		bus:        bus,
		deps:       map[string]*deployment{},
		containers: map[string]containerRef{},
	}
	if l, ok := rt.(runtime.StateListener); ok {
		l.OnStateChange(e.onContainerState)
	}
	return e
}

// --- read side ------------------------------------------------------------

// Deployments lists every applied topology, sorted by name.
func (e *Engine) Deployments() []DeploymentInfo {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]DeploymentInfo, 0, len(e.deps))
	for _, d := range e.deps {
		out = append(out, e.infoLocked(d))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Current returns the applied topology of one deployment.
func (e *Engine) Current(name string) (spec.Topology, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	d := e.deps[name]
	if d == nil {
		return spec.Topology{}, false
	}
	return d.current.Clone(), true
}

func (e *Engine) infoLocked(d *deployment) DeploymentInfo {
	st := make(map[string]NodeStatus, len(d.statuses))
	for k, v := range d.statuses {
		st[k] = *v
	}
	return DeploymentInfo{Name: d.name, Phase: d.phase, Network: d.network, Topology: d.current.Clone(), Statuses: st}
}

// MetricInputs feeds the mock metrics ticker so its numbers react to what
// the engine is doing, summed over every deployment.
type MetricInputs struct {
	Running      bool
	Total        int
	Healthy      int
	ActiveFaults int
}

func (e *Engine) MetricInputs() MetricInputs {
	e.mu.Lock()
	defer e.mu.Unlock()
	var in MetricInputs
	for _, d := range e.deps {
		if d.phase == PhaseRunning || d.phase == PhaseApplying {
			in.Running = true
		}
		for _, s := range d.statuses {
			in.Total++
			if s.State == string(runtime.StateHealthy) {
				in.Healthy++
			}
		}
		for _, fs := range d.faults {
			in.ActiveFaults += len(fs)
		}
	}
	return in
}

// LinkInput is what the metrics ticker needs to judge one edge of a
// deployment: the runtime state of the nodes at both ends and the faults
// currently active on them.
type LinkInput struct {
	Deployment string
	EdgeID     string
	States     []string // node states of from, to and (if set) via
	Faults     []spec.FaultSpec
}

// LinkInputs returns one entry per edge of every deployment that is applying
// or running.
func (e *Engine) LinkInputs() []LinkInput {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []LinkInput
	for _, d := range e.deps {
		if d.phase != PhaseRunning && d.phase != PhaseApplying {
			continue
		}
		for _, edge := range d.current.Edges {
			in := LinkInput{Deployment: d.name, EdgeID: edge.ID}
			for _, id := range []string{edge.From, edge.To, edge.Via} {
				if id == "" {
					continue
				}
				state := string(runtime.StatePending)
				if st := d.statuses[id]; st != nil {
					state = st.State
				}
				in.States = append(in.States, state)
				in.Faults = append(in.Faults, d.faults[id]...)
			}
			out = append(out, in)
		}
	}
	return out
}

// --- plan / apply ---------------------------------------------------------

// Plan validates and diffs against what is running under the topology's
// name, without executing. Validation findings are returned alongside the
// plan; the plan is empty when there are errors.
func (e *Engine) Plan(desired spec.Topology) (planner.Plan, []spec.ValidationError) {
	desired = desired.Normalized()
	findings := desired.Validate()
	if spec.HasErrors(findings) {
		return planner.Plan{Ops: []planner.Operation{}}, findings
	}
	current, _ := e.Current(desired.Name)
	if current.Name == "" {
		current = spec.Topology{Version: desired.Version, Name: desired.Name}.Normalized()
	}
	return planner.Diff(current, desired), findings
}

// Apply runs the full pipeline for the deployment named after the topology,
// creating it on first apply and diffing against it afterwards. It blocks
// until the plan has executed and streams apply:progress / node:status /
// engine:log events meanwhile. Other deployments are untouched.
func (e *Engine) Apply(ctx context.Context, desired spec.Topology) error {
	desired = desired.Normalized()
	findings := desired.Validate()
	if spec.HasErrors(findings) {
		msgs := []string{}
		for _, f := range findings {
			if f.Severity == spec.SeverityError {
				msgs = append(msgs, f.Error())
			}
		}
		e.log("error", "engine", fmt.Sprintf("[%s] validation failed: %s", desired.Name, strings.Join(msgs, "; ")))
		if d := e.lookup(desired.Name); d != nil {
			e.setPhase(d, PhaseFailed, "validation failed")
		}
		return fmt.Errorf("%w: %s", ErrValidation, strings.Join(msgs, "; "))
	}

	d, err := e.begin(desired.Name, true)
	if err != nil {
		return err
	}
	defer e.end(d)

	e.setPhase(d, PhaseValidating, "")
	for _, f := range findings {
		e.log("warn", "validate", fmt.Sprintf("[%s] %s", d.name, f.Error()))
	}

	e.setPhase(d, PhasePlanning, "")
	current, _ := e.Current(d.name)
	plan := planner.Diff(current, desired)
	e.log("info", "planner", fmt.Sprintf("[%s] %d ops (%d hot, %d cold, %d structural)",
		d.name, len(plan.Ops), plan.Summary.Hot, plan.Summary.Cold, plan.Summary.Structural))

	e.setPhase(d, PhaseApplying, "")
	start := time.Now()
	if err := e.execute(ctx, d, plan, desired); err != nil {
		e.setPhase(d, PhaseFailed, err.Error())
		return err
	}

	e.mu.Lock()
	d.current = desired
	d.network = plan.Network
	e.mu.Unlock()
	e.log("info", "engine", fmt.Sprintf("[%s] apply complete in %s", d.name, time.Since(start).Round(time.Millisecond)))
	if len(desired.Nodes) == 0 {
		e.remove(d)
	} else {
		e.setPhase(d, PhaseRunning, "")
	}
	return nil
}

// Teardown destroys everything running under the named topology and forgets
// the deployment.
func (e *Engine) Teardown(ctx context.Context, name string) error {
	d, err := e.begin(name, false)
	if err != nil {
		return err
	}
	defer e.end(d)

	cur, _ := e.Current(name)
	empty := spec.Topology{Version: cur.Version, Name: cur.Name}.Normalized()
	plan := planner.Diff(cur, empty)
	e.setPhase(d, PhaseTearingDown, "")
	e.log("info", "engine", fmt.Sprintf("[%s] teardown: %d ops", d.name, len(plan.Ops)))
	if err := e.execute(ctx, d, plan, empty); err != nil {
		e.setPhase(d, PhaseFailed, err.Error())
		return err
	}
	e.mu.Lock()
	d.current = empty
	d.faults = map[string][]spec.FaultSpec{}
	e.mu.Unlock()
	e.remove(d)
	return nil
}

func (e *Engine) execute(ctx context.Context, d *deployment, plan planner.Plan, desired spec.Topology) error {
	for i, op := range plan.Ops {
		started := time.Now()
		e.progress(d, op, i, len(plan.Ops), "started", "", 0)
		err := e.executeOp(ctx, d, op, plan.Network, desired)
		elapsed := time.Since(started).Milliseconds()
		if err != nil {
			e.progress(d, op, i, len(plan.Ops), "failed", err.Error(), elapsed)
			e.log("error", "engine", fmt.Sprintf("[%s] %s %s failed: %v", d.name, op.Kind, op.Target, err))
			return fmt.Errorf("%s %s: %w", op.Kind, op.Target, err)
		}
		e.progress(d, op, i, len(plan.Ops), "done", "", elapsed)
	}
	return nil
}

func (e *Engine) executeOp(ctx context.Context, d *deployment, op planner.Operation, network string, desired spec.Topology) error {
	switch op.Kind {
	case planner.OpEnsureNetwork:
		return e.rt.EnsureNetwork(ctx, op.Target)

	case planner.OpCreate:
		node, ok := desired.NodeByID(op.Target)
		if !ok {
			return fmt.Errorf("node %q not in desired topology", op.Target)
		}
		e.setStatus(d, node.ID, string(runtime.StatePending), node.Replicas, 0, "")
		for r := 1; r <= node.Replicas; r++ {
			name := containerName(d.slug, node.ID, r, node.Replicas)
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
			e.containers[name] = containerRef{dep: d.name, node: node.ID}
			e.mu.Unlock()
			if _, err := e.rt.Create(ctx, cspec); err != nil {
				return err
			}
			e.log("info", "runtime", fmt.Sprintf("[%s] created %s (%s)", d.name, name, node.Image))
			if err := e.rt.Start(ctx, name); err != nil {
				return err
			}
		}
		return nil

	case planner.OpWaitHealthy:
		return e.waitHealthy(ctx, d, op.Target)

	case planner.OpEnsureTopic:
		// Seam: a real implementation talks to the broker's admin API
		// (e.g. kafka CreateTopics) using op.Params["broker"] / ["topic"].
		if err := sleepCtx(ctx, 80*time.Millisecond); err != nil {
			return err
		}
		e.log("info", "broker", fmt.Sprintf("[%s] ensured topic %v on %v", d.name, op.Params["topic"], op.Params["broker"]))
		return nil

	case planner.OpControl:
		// Seam: push the patch to the running container (HTTP admin endpoint,
		// SIGHUP + config file, or toxiproxy API for proxy nodes).
		if err := sleepCtx(ctx, 40*time.Millisecond); err != nil {
			return err
		}
		e.log("info", "control", fmt.Sprintf("[%s] %s: %s", d.name, op.Target, op.Reason))
		return nil

	case planner.OpDestroy:
		names := e.containersFor(d.name, op.Target)
		for _, name := range names {
			if err := e.rt.Destroy(ctx, name); err != nil && !errors.Is(err, runtime.ErrNotFound) {
				return err
			}
			e.mu.Lock()
			delete(e.containers, name)
			e.mu.Unlock()
			e.log("info", "runtime", fmt.Sprintf("[%s] destroyed %s", d.name, name))
		}
		e.mu.Lock()
		delete(d.statuses, op.Target)
		delete(d.faults, op.Target)
		e.mu.Unlock()
		e.bus.Publish(events.NodeStatus, events.NodeStatusPayload{
			Deployment: d.name, NodeID: op.Target, State: "removed", UpdatedAt: time.Now().UnixMilli(),
		})
		return nil
	}
	return fmt.Errorf("unknown op kind %q", op.Kind)
}

func (e *Engine) waitHealthy(ctx context.Context, d *deployment, nodeID string) error {
	deadline := time.Now().Add(healthyTimeout)
	for {
		names := e.containersFor(d.name, nodeID)
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
// deployment's current topology so a later Diff sees no drift.
func (e *Engine) SetHotConfig(ctx context.Context, depName, nodeID string, patch map[string]any) error {
	e.mu.Lock()
	d := e.deps[depName]
	if d == nil {
		e.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrNotDeployed, depName)
	}
	idx := -1
	for i, n := range d.current.Nodes {
		if n.ID == nodeID {
			idx = i
		}
	}
	if idx < 0 || d.statuses[nodeID] == nil {
		e.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrNodeNotRunning, nodeID)
	}
	node := &d.current.Nodes[idx]
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
	e.progress(d, op, 0, 1, "started", "", 0)
	// Seam: same control transport as executeOp(OpControl).
	e.progress(d, op, 0, 1, "done", "", 0)
	parts := []string{}
	for k, v := range changed {
		parts = append(parts, fmt.Sprintf("%s=%s", k, spec.CanonValue(v)))
	}
	sort.Strings(parts)
	e.log("info", "control", fmt.Sprintf("[%s] %s: %s (no restart)", d.name, nodeID, strings.Join(parts, ", ")))
	return nil
}

// InjectFault records a fault against a running node. Kill and pause are
// executed through the runtime because they are pure container operations;
// latency, drop, partition and cpu_stress are logged only until the proxy /
// tc / stress seams exist.
func (e *Engine) InjectFault(ctx context.Context, depName, nodeID string, fault spec.FaultSpec) error {
	if !fault.Kind.Valid() {
		return fmt.Errorf("engine: unknown fault kind %q", fault.Kind)
	}
	e.mu.Lock()
	d := e.deps[depName]
	if d == nil {
		e.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrNotDeployed, depName)
	}
	if d.statuses[nodeID] == nil {
		e.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrNodeNotRunning, nodeID)
	}
	d.faults[nodeID] = append(d.faults[nodeID], fault)
	e.mu.Unlock()

	names := e.containersFor(d.name, nodeID)
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
	e.log("warn", "fault", fmt.Sprintf("[%s] injected %s into %s (%dms) %v", d.name, fault.Kind, nodeID, fault.DurationMs, fault.Params))
	e.bus.Publish(events.FaultInjected, events.FaultPayload{Deployment: d.name, NodeID: nodeID, Kind: string(fault.Kind), DurationMs: fault.DurationMs, Active: true})
	e.setStatusMessage(d, nodeID, "fault: "+string(fault.Kind))

	if fault.DurationMs > 0 {
		time.AfterFunc(time.Duration(fault.DurationMs)*time.Millisecond, func() {
			e.clearFault(d, nodeID, fault)
		})
	}
	return nil
}

func (e *Engine) clearFault(d *deployment, nodeID string, fault spec.FaultSpec) {
	e.mu.Lock()
	if e.deps[d.name] != d {
		// torn down in the meantime; nothing left to clear
		e.mu.Unlock()
		return
	}
	fs := d.faults[nodeID]
	for i, f := range fs {
		if f.Kind == fault.Kind {
			d.faults[nodeID] = append(fs[:i], fs[i+1:]...)
			break
		}
	}
	e.mu.Unlock()
	ctx := context.Background()
	for _, n := range e.containersFor(d.name, nodeID) {
		switch fault.Kind {
		case spec.FaultPause:
			_ = e.rt.Unpause(ctx, n)
		case spec.FaultKill:
			_ = e.rt.Start(ctx, n)
		}
	}
	e.log("info", "fault", fmt.Sprintf("[%s] cleared %s on %s", d.name, fault.Kind, nodeID))
	e.bus.Publish(events.FaultInjected, events.FaultPayload{Deployment: d.name, NodeID: nodeID, Kind: string(fault.Kind), Active: false})
	e.setStatusMessage(d, nodeID, "")
}

// --- status bookkeeping ---------------------------------------------------

func (e *Engine) onContainerState(c runtime.Container) {
	e.mu.Lock()
	ref, ok := e.containers[c.Name]
	d := e.deps[ref.dep]
	e.mu.Unlock()
	if !ok || d == nil {
		return
	}
	e.recomputeStatus(d, ref.node)
}

func (e *Engine) recomputeStatus(d *deployment, nodeID string) {
	names := e.containersFor(d.name, nodeID)
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
	st := d.statuses[nodeID]
	if st == nil {
		e.mu.Unlock()
		return
	}
	replicas := st.Replicas
	msg := st.Message
	e.mu.Unlock()
	e.setStatus(d, nodeID, string(worst), replicas, ready, msg)
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

func (e *Engine) setStatus(d *deployment, nodeID, state string, replicas, ready int, msg string) {
	e.mu.Lock()
	st := &NodeStatus{NodeID: nodeID, State: state, Replicas: replicas, Ready: ready, Message: msg, UpdatedAt: time.Now().UnixMilli()}
	d.statuses[nodeID] = st
	payload := statusPayload(d.name, *st)
	e.mu.Unlock()
	e.bus.Publish(events.NodeStatus, payload)
}

func (e *Engine) setStatusMessage(d *deployment, nodeID, msg string) {
	e.mu.Lock()
	st := d.statuses[nodeID]
	if st == nil {
		e.mu.Unlock()
		return
	}
	st.Message = msg
	st.UpdatedAt = time.Now().UnixMilli()
	payload := statusPayload(d.name, *st)
	e.mu.Unlock()
	e.bus.Publish(events.NodeStatus, payload)
}

func statusPayload(dep string, st NodeStatus) events.NodeStatusPayload {
	return events.NodeStatusPayload{
		Deployment: dep, NodeID: st.NodeID, State: st.State, Replicas: st.Replicas,
		Ready: st.Ready, Message: st.Message, UpdatedAt: st.UpdatedAt,
	}
}

func (e *Engine) containersFor(depName, nodeID string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for name, ref := range e.containers {
		if ref.dep == depName && ref.node == nodeID {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func (e *Engine) lookup(name string) *deployment {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.deps[name]
}

// begin marks a deployment as busy, creating it when create is set. Two
// operations on the same deployment cannot overlap; different deployments
// can.
func (e *Engine) begin(name string, create bool) (*deployment, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	d := e.deps[name]
	if d == nil {
		if !create {
			return nil, fmt.Errorf("%w: %s", ErrNotDeployed, name)
		}
		d = &deployment{
			name:     name,
			slug:     spec.Slug(name),
			phase:    PhaseIdle,
			current:  spec.Topology{Version: spec.CurrentVersion, Name: name}.Normalized(),
			statuses: map[string]*NodeStatus{},
			faults:   map[string][]spec.FaultSpec{},
		}
		e.deps[name] = d
	}
	if d.applying {
		return nil, ErrBusy
	}
	d.applying = true
	return d, nil
}

func (e *Engine) end(d *deployment) {
	e.mu.Lock()
	d.applying = false
	e.mu.Unlock()
}

// remove forgets a deployment that has nothing running any more.
func (e *Engine) remove(d *deployment) {
	e.mu.Lock()
	if e.deps[d.name] == d {
		delete(e.deps, d.name)
	}
	for name, ref := range e.containers {
		if ref.dep == d.name {
			delete(e.containers, name)
		}
	}
	e.mu.Unlock()
	e.bus.Publish(events.EnginePhase, events.PhasePayload{Deployment: d.name, Phase: string(PhaseGone)})
}

func (e *Engine) setPhase(d *deployment, p Phase, msg string) {
	e.mu.Lock()
	d.phase = p
	e.mu.Unlock()
	e.bus.Publish(events.EnginePhase, events.PhasePayload{Deployment: d.name, Phase: string(p), Message: msg})
}

func (e *Engine) progress(d *deployment, op planner.Operation, idx, count int, status, msg string, elapsed int64) {
	e.bus.Publish(events.ApplyProgress, events.ProgressPayload{
		Deployment: d.name,
		OpID:       op.ID, Index: idx, Count: count, Kind: string(op.Kind), Target: op.Target,
		Class: string(op.Class), Status: status, Message: msg, Elapsed: elapsed,
	})
}

func (e *Engine) log(level, source, msg string) {
	e.bus.Log(level, source, msg)
}

func containerName(slug, nodeID string, replica, total int) string {
	if total <= 1 {
		return slug + "-" + nodeID
	}
	return fmt.Sprintf("%s-%s-%d", slug, nodeID, replica)
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
