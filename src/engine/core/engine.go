// Package core drives topologies from desired state to running state:
// validate -> diff -> plan -> execute against a Runtime, emitting progress
// on the event bus. Several topologies can be applied at once; each one is a
// deployment with its own network, containers, statuses and faults, keyed by
// the topology's name. The engine also owns the hot-config, fault-injection
// and control entry points used by the UI, talking to the blocks' admin
// ports through blockclient.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"symphony/blocks/common/control"
	"symphony/blocks/common/health"
	"symphony/engine/blockclient"
	"symphony/engine/events"
	"symphony/engine/images"
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
// its replicas. State is the "worst" replica state; Checks come from the
// blocks' deep health.
type NodeStatus struct {
	NodeID    string               `json:"node_id"`
	State     string               `json:"state"` // pending | starting | healthy | degraded | failed | stopped | paused
	Replicas  int                  `json:"replicas"`
	Ready     int                  `json:"ready"`
	Message   string               `json:"message,omitempty"`
	UpdatedAt int64                `json:"updated_at"`
	Checks    []events.HealthCheck `json:"checks,omitempty"`
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
	ErrNoAdmin        = errors.New("engine: node has no reachable admin port")
)

const (
	healthyTimeout = 90 * time.Second // image builds happen before, so this is just startup
	healthyPoll    = 150 * time.Millisecond
	pollInterval   = time.Second
)

// deployment is everything the engine tracks for one applied topology.
// Container names are prefixed with the slug so two deployments that share
// node ids (every preset has a "gateway") never collide in the runtime.
type deployment struct {
	name     string
	slug     string
	network  string
	token    string
	phase    Phase
	current  spec.Topology
	statuses map[string]*NodeStatus
	faults   map[string][]activeFault
	applying bool
	// last runtime state per container, from events / inspect
	states map[string]runtime.Container
	// last stats sample per container, for rates
	stats map[string]statSample
}

type activeFault struct {
	spec.FaultSpec
	ID    string // block-side fault id, "" for runtime faults
	Until time.Time
}

type statSample struct {
	runtime.Stats
	at time.Time
}

// containerRef says which deployment and node a runtime container belongs to.
type containerRef struct {
	dep  string
	node string
}

// Options configure the engine.
type Options struct {
	Runtime runtime.Runtime
	Bus     *events.Bus
	Images  *images.Ensurer
	Store   *Store
}

// Engine is safe for concurrent use; Wails invokes bound methods from
// multiple goroutines, and different deployments may apply at the same time.
type Engine struct {
	rt     runtime.Runtime
	bus    *events.Bus
	images *images.Ensurer
	store  *Store

	mu         sync.Mutex
	deps       map[string]*deployment
	containers map[string]containerRef
	clients    map[string]*blockclient.Client // container name -> admin client

	pollStop context.CancelFunc
}

func New(opts Options) *Engine {
	e := &Engine{
		rt:         opts.Runtime,
		bus:        opts.Bus,
		images:     opts.Images,
		store:      opts.Store,
		deps:       map[string]*deployment{},
		containers: map[string]containerRef{},
		clients:    map[string]*blockclient.Client{},
	}
	if e.images == nil {
		e.images = images.NewEnsurer(nil)
	}
	if e.store == nil {
		e.store = NewStore("")
	}
	if l, ok := e.rt.(runtime.StateListener); ok {
		l.OnStateChange(e.onContainerState)
	}
	return e
}

// Runtime exposes the backend (the API reports its kind).
func (e *Engine) Runtime() runtime.Runtime { return e.rt }

// Start begins the health/stats poller. Stop ends it.
func (e *Engine) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	e.mu.Lock()
	e.pollStop = cancel
	e.mu.Unlock()
	go e.pollLoop(ctx)
}

func (e *Engine) Stop() {
	e.mu.Lock()
	if e.pollStop != nil {
		e.pollStop()
		e.pollStop = nil
	}
	e.mu.Unlock()
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

// NodeSnapshot is what the collector scrapes for one node.
type NodeSnapshot struct {
	ID         string
	Kind       spec.NodeKind
	State      string
	Clients    []*blockclient.Client
	Containers []string
	CPUPct     float64
	Faults     int
}

// DeploymentSnapshot is a live deployment as the collector sees it.
type DeploymentSnapshot struct {
	Name     string
	Phase    Phase
	Topology spec.Topology
	Nodes    map[string]NodeSnapshot
	Faults   int
}

// Snapshot returns every live deployment with its admin clients.
func (e *Engine) Snapshot() []DeploymentSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []DeploymentSnapshot
	for _, d := range e.deps {
		if d.phase != PhaseRunning && d.phase != PhaseApplying && d.phase != PhaseFailed {
			continue
		}
		snap := DeploymentSnapshot{Name: d.name, Phase: d.phase, Topology: d.current.Clone(), Nodes: map[string]NodeSnapshot{}}
		for _, n := range d.current.Nodes {
			ns := NodeSnapshot{ID: n.ID, Kind: n.Kind, State: "pending"}
			if st := d.statuses[n.ID]; st != nil {
				ns.State = st.State
			}
			for _, name := range planner.ContainerNames(d.slug, n) {
				if e.containers[name].dep != d.name {
					continue
				}
				ns.Containers = append(ns.Containers, name)
				if c := e.clients[name]; c != nil {
					ns.Clients = append(ns.Clients, c)
				}
				if s, ok := d.stats[name]; ok {
					ns.CPUPct += s.CPUPercent
				}
			}
			ns.Faults = len(d.faults[n.ID])
			snap.Faults += ns.Faults
			snap.Nodes[n.ID] = ns
		}
		out = append(out, snap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// --- plan / apply ---------------------------------------------------------

// Plan validates and diffs against what is running under the topology's
// name, without executing.
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
// creating it on first apply and diffing against it afterwards.
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
	if err := e.store.save(d.slug, record{Name: d.name, Token: d.token, Network: d.network, Topology: desired}); err != nil {
		e.log("warn", "engine", fmt.Sprintf("[%s] could not remember deployment on disk: %v", d.name, err))
	}
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
	if err := e.rt.RemoveNetwork(ctx, d.network); err != nil {
		e.log("warn", "runtime", fmt.Sprintf("[%s] %v", d.name, err))
	}
	e.mu.Lock()
	d.current = empty
	d.faults = map[string][]activeFault{}
	e.mu.Unlock()
	e.remove(d)
	return nil
}

// TeardownAll tears down every deployment (shutdown with the "clean" option).
func (e *Engine) TeardownAll(ctx context.Context) {
	for _, d := range e.Deployments() {
		if err := e.Teardown(ctx, d.Name); err != nil {
			e.log("warn", "engine", fmt.Sprintf("[%s] teardown: %v", d.Name, err))
		}
	}
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
	case planner.OpEnsureImage:
		return e.images.Ensure(ctx, e.rt, op.Target, func(line string) {
			e.log("info", "image", fmt.Sprintf("[%s] %s", d.name, line))
		})

	case planner.OpEnsureNetwork:
		return e.rt.EnsureNetwork(ctx, op.Target)

	case planner.OpCreate:
		node, ok := desired.NodeByID(op.Target)
		if !ok {
			return fmt.Errorf("node %q not in desired topology", op.Target)
		}
		e.setStatus(d, node.ID, string(runtime.StatePending), node.Replicas, 0, "", nil)
		links := planner.LinkSet(desired, node.ID, d.slug)
		linksJSON, _ := json.Marshal(links)
		cfgJSON, _ := json.Marshal(mergedConfig(node))
		for r := 1; r <= node.Replicas; r++ {
			name := planner.ContainerName(d.slug, node.ID, r, node.Replicas)
			env := map[string]string{
				"SYMPHONY_NODE":        node.ID,
				"SYMPHONY_KIND":        string(node.Kind),
				"SYMPHONY_DEPLOYMENT":  d.slug,
				"SYMPHONY_REPLICA":     fmt.Sprint(r),
				"SYMPHONY_REPLICAS":    fmt.Sprint(node.Replicas),
				"SYMPHONY_LINKS":       string(linksJSON),
				"SYMPHONY_CONFIG":      string(cfgJSON),
				"SYMPHONY_ADMIN_TOKEN": d.token,
			}
			if len(node.Behaviour) > 0 {
				env["SYMPHONY_BEHAVIOUR"] = string(node.Behaviour)
			}
			for k, v := range node.Config.Cold {
				env["CFG_"+strings.ToUpper(k)] = fmt.Sprintf("%v", v)
			}
			cspec := runtime.ContainerSpec{
				Name:    name,
				Image:   node.Image,
				Network: network,
				Alias:   node.ID,
				Env:     env,
				Labels: map[string]string{
					runtime.LabelNode:       node.ID,
					runtime.LabelKind:       string(node.Kind),
					runtime.LabelDeployment: d.slug,
					runtime.LabelTopology:   desired.Name,
				},
				Resources:   runtime.Resources{CPUs: node.Resources.CPUs, MemoryMB: node.Resources.MemoryMB, PidsMax: node.Resources.PidsMax},
				AdminPort:   planner.AdminPort,
				DataPort:    planner.DataPort,
				Healthcheck: spec.IsBlockImage(node.Image),
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
			e.attachClient(ctx, d, name)
		}
		return nil

	case planner.OpWaitHealthy:
		return e.waitHealthy(ctx, d, op.Target)

	case planner.OpEnsureTopic:
		// Topics exist in the broker block from its links; nothing to do
		// beyond checking the broker answers.
		broker, _ := op.Params["broker"].(string)
		if c := e.anyClient(d, broker); c != nil {
			if _, err := c.Control(ctx, control.Command{Op: "topic.create", Params: map[string]any{"topic": op.Params["topic"]}}); err != nil {
				return err
			}
		}
		e.log("info", "broker", fmt.Sprintf("[%s] ensured topic %v on %v", d.name, op.Params["topic"], op.Params["broker"]))
		return nil

	case planner.OpControl:
		return e.pushConfig(ctx, d, op.Target, op.Params)

	case planner.OpConfigure:
		prog, _ := op.Params["behaviour"]
		cmd := control.Command{Op: "behaviour.reset"}
		if prog != nil {
			raw, _ := json.Marshal(prog)
			var params map[string]any
			_ = json.Unmarshal(raw, &params)
			cmd = control.Command{Op: "behaviour.set", Params: params}
		}
		for _, c := range e.clientsFor(d, op.Target) {
			if _, err := c.Control(ctx, cmd); err != nil {
				return err
			}
		}
		e.log("info", "control", fmt.Sprintf("[%s] %s: behaviour updated", d.name, op.Target))
		return nil

	case planner.OpUpdateResources:
		node, ok := desired.NodeByID(op.Target)
		if !ok {
			return fmt.Errorf("node %q not in desired topology", op.Target)
		}
		for _, name := range e.containersFor(d.name, op.Target) {
			if err := e.rt.Update(ctx, name, runtime.Resources{CPUs: node.Resources.CPUs, MemoryMB: node.Resources.MemoryMB, PidsMax: node.Resources.PidsMax}); err != nil {
				return err
			}
		}
		e.log("info", "runtime", fmt.Sprintf("[%s] %s: %s", d.name, op.Target, op.Reason))
		return nil

	case planner.OpDestroy:
		names := e.containersFor(d.name, op.Target)
		for _, name := range names {
			if err := e.rt.Destroy(ctx, name); err != nil && !errors.Is(err, runtime.ErrNotFound) {
				return err
			}
			e.mu.Lock()
			delete(e.containers, name)
			delete(e.clients, name)
			delete(d.states, name)
			delete(d.stats, name)
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

// mergedConfig is what SYMPHONY_CONFIG carries: hot and cold keys together.
func mergedConfig(n spec.Node) map[string]any {
	out := map[string]any{}
	for k, v := range n.Config.Cold {
		out[k] = v
	}
	for k, v := range n.Config.Hot {
		out[k] = v
	}
	return out
}

// attachClient registers the admin client of a started container when the
// runtime published its admin port.
func (e *Engine) attachClient(ctx context.Context, d *deployment, name string) {
	c, err := e.rt.Inspect(ctx, name)
	if err != nil || c.AdminAddr == "" {
		return
	}
	e.mu.Lock()
	e.clients[name] = blockclient.New(c.AdminAddr, d.token)
	d.states[name] = c
	e.mu.Unlock()
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
			e.mu.Lock()
			d.states[name] = c
			cl := e.clients[name]
			e.mu.Unlock()
			if c.State == runtime.StateFailed || c.State == runtime.StateStopped {
				logs, _ := e.rt.Logs(ctx, name, 20)
				for _, l := range logs {
					e.log("error", "container", fmt.Sprintf("[%s] %s: %s", d.name, name, l))
				}
				return fmt.Errorf("container %s failed to start (exit %d, %s)", name, c.ExitCode, c.Error)
			}
			if cl == nil && c.AdminAddr != "" {
				e.attachClient(ctx, d, name)
				e.mu.Lock()
				cl = e.clients[name]
				e.mu.Unlock()
			}
			if cl != nil {
				// the block answers for itself, no need to wait for Docker's
				// slower healthcheck cadence
				if doc, err := cl.Health(ctx); err == nil && doc.Status == health.Healthy {
					healthy++
				}
				continue
			}
			if c.State == runtime.StateHealthy {
				healthy++
			}
		}
		e.recomputeStatus(d, nodeID)
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

// --- hot config / faults / control ------------------------------------------

// pushConfig sends a hot patch to every replica of a node.
func (e *Engine) pushConfig(ctx context.Context, d *deployment, nodeID string, patch map[string]any) error {
	clients := e.clientsFor(d, nodeID)
	if len(clients) == 0 {
		if err := sleepCtx(ctx, 40*time.Millisecond); err != nil {
			return err
		}
		e.log("info", "control", fmt.Sprintf("[%s] %s: %s (no admin port, recorded only)", d.name, nodeID, describe(patch)))
		return nil
	}
	for _, c := range clients {
		if _, err := c.Control(ctx, control.Command{Op: "config.set", Params: patch}); err != nil {
			return err
		}
	}
	e.log("info", "control", fmt.Sprintf("[%s] %s: %s (no restart)", d.name, nodeID, describe(patch)))
	return nil
}

func describe(patch map[string]any) string {
	parts := []string{}
	for k, v := range patch {
		parts = append(parts, fmt.Sprintf("%s=%s", k, spec.CanonValue(v)))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

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
	op := planner.Operation{ID: "control-" + nodeID, Kind: planner.OpControl, Target: nodeID, Class: planner.ClassHot, Params: changed}
	e.progress(d, op, 0, 1, "started", "", 0)
	if err := e.pushConfig(ctx, d, nodeID, changed); err != nil {
		e.progress(d, op, 0, 1, "failed", err.Error(), 0)
		return err
	}
	e.progress(d, op, 0, 1, "done", "", 0)
	_ = e.store.save(d.slug, record{Name: d.name, Token: d.token, Network: d.network, Topology: e.currentLocked(d)})
	return nil
}

func (e *Engine) currentLocked(d *deployment) spec.Topology {
	e.mu.Lock()
	defer e.mu.Unlock()
	return d.current.Clone()
}

// SetResources changes a running node's CPU/RAM limits in place.
func (e *Engine) SetResources(ctx context.Context, depName, nodeID string, r spec.Resources) error {
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
	if idx < 0 {
		e.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrNodeNotRunning, nodeID)
	}
	d.current.Nodes[idx].Resources = r
	e.mu.Unlock()
	for _, name := range e.containersFor(d.name, nodeID) {
		if err := e.rt.Update(ctx, name, runtime.Resources{CPUs: r.CPUs, MemoryMB: r.MemoryMB, PidsMax: r.PidsMax}); err != nil {
			return err
		}
	}
	e.log("info", "runtime", fmt.Sprintf("[%s] %s: resources cpus=%g memory_mb=%d", d.name, nodeID, r.CPUs, r.MemoryMB))
	_ = e.store.save(d.slug, record{Name: d.name, Token: d.token, Network: d.network, Topology: e.currentLocked(d)})
	return nil
}

// Control sends a raw command to every replica of a node and returns the
// first replica's result (the UI's escape hatch: load generator, behaviour
// editor, snapshots).
func (e *Engine) Control(ctx context.Context, depName, nodeID string, cmd control.Command) (control.Result, error) {
	d := e.lookup(depName)
	if d == nil {
		return control.Result{}, fmt.Errorf("%w: %s", ErrNotDeployed, depName)
	}
	clients := e.clientsFor(d, nodeID)
	if len(clients) == 0 {
		return control.Result{}, fmt.Errorf("%w: %s", ErrNoAdmin, nodeID)
	}
	var first control.Result
	for i, c := range clients {
		res, err := c.Control(ctx, cmd)
		if err != nil {
			return res, err
		}
		if i == 0 {
			first = res
		}
	}
	if cmd.Op != "load.status" && cmd.Op != "sagas.recent" && cmd.Op != "sagas.summary" && cmd.Op != "fault.list" && cmd.Op != "config.get" && cmd.Op != "behaviour.get" && cmd.Op != "topics.get" {
		e.log("info", "control", fmt.Sprintf("[%s] %s: %s", d.name, nodeID, cmd.Op))
	}
	return first, nil
}

// Logs returns the last n log lines of a node's containers (block log
// buffer when reachable, container logs otherwise).
func (e *Engine) Logs(ctx context.Context, depName, nodeID string, n int) ([]blockclient.LogLine, error) {
	d := e.lookup(depName)
	if d == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotDeployed, depName)
	}
	var out []blockclient.LogLine
	for _, name := range e.containersFor(d.name, nodeID) {
		e.mu.Lock()
		c := e.clients[name]
		e.mu.Unlock()
		if c != nil {
			lines, err := c.Logs(ctx, n)
			if err == nil {
				for i := range lines {
					if lines[i].Attrs == nil {
						lines[i].Attrs = map[string]any{}
					}
					lines[i].Attrs["container"] = name
				}
				out = append(out, lines...)
				continue
			}
			e.log("debug", "logs", fmt.Sprintf("[%s] %s: block logs unavailable (%v), reading container logs", d.name, name, err))
		}
		raw, err := e.rt.Logs(ctx, name, n)
		if err != nil {
			return nil, err
		}
		for _, l := range raw {
			out = append(out, blockclient.LogLine{Ts: time.Now().UnixMilli(), Level: "INFO", Msg: l, Attrs: map[string]any{"container": name}})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Ts < out[j].Ts })
	if len(out) > n && n > 0 {
		out = out[len(out)-n:]
	}
	return out, nil
}

// InjectFault injects a fault into a running node: container-level kinds
// through the runtime, everything else through the block's /control.
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
	e.mu.Unlock()
	if fault.Params == nil {
		fault.Params = map[string]any{}
	}
	dur := time.Duration(fault.DurationMs) * time.Millisecond
	names := e.containersFor(d.name, nodeID)
	clients := e.clientsFor(d, nodeID)
	af := activeFault{FaultSpec: fault}
	if dur > 0 {
		af.Until = time.Now().Add(dur)
	}

	switch fault.Kind {
	case spec.FaultPause:
		for _, n := range names {
			if err := e.rt.Pause(ctx, n); err != nil {
				return err
			}
		}
	case spec.FaultKill:
		for _, n := range names {
			if err := e.rt.Kill(ctx, n); err != nil {
				return err
			}
		}
	case spec.FaultPartition:
		if _, selective := fault.Params["peers"]; selective && len(clients) > 0 {
			id, err := e.blockFault(ctx, clients, fault, dur)
			if err != nil {
				return err
			}
			af.ID = id
		} else {
			for _, n := range names {
				if err := e.rt.Disconnect(ctx, n, d.network); err != nil {
					return err
				}
			}
		}
	default:
		if len(clients) == 0 {
			e.log("warn", "fault", fmt.Sprintf("[%s] %s has no admin port; %s recorded only", d.name, nodeID, fault.Kind))
		} else if fault.Kind.Chaos() {
			cmd := control.Command{DurationMs: fault.DurationMs, Params: fault.Params}
			switch fault.Kind {
			case spec.FaultCPU:
				cmd.Op = "chaos.cpu"
			case spec.FaultMemory:
				cmd.Op = "chaos.memory"
			case spec.FaultStall:
				cmd.Op = "chaos.stall"
			case spec.FaultCrash:
				cmd.Op = "chaos.crash"
			}
			for _, c := range clients {
				if _, err := c.Control(ctx, cmd); err != nil {
					return err
				}
			}
		} else {
			id, err := e.blockFault(ctx, clients, fault, dur)
			if err != nil {
				return err
			}
			af.ID = id
		}
	}

	e.mu.Lock()
	d.faults[nodeID] = append(d.faults[nodeID], af)
	e.mu.Unlock()
	e.log("warn", "fault", fmt.Sprintf("[%s] injected %s into %s (%dms) %v", d.name, fault.Kind, nodeID, fault.DurationMs, fault.Params))
	e.setStatusMessage(d, nodeID, "fault: "+string(fault.Kind))

	if dur > 0 && fault.Kind != spec.FaultCrash {
		time.AfterFunc(dur, func() { e.clearFault(d, nodeID, af, true) })
	}
	return nil
}

// blockFault translates a FaultSpec into a fault.set command and returns
// the block-side fault id (of the first replica).
func (e *Engine) blockFault(ctx context.Context, clients []*blockclient.Client, fault spec.FaultSpec, dur time.Duration) (string, error) {
	params := map[string]any{"kind": string(fault.Kind)}
	var target control.Command
	for k, v := range fault.Params {
		switch k {
		case "edge":
			target.Target.Edge, _ = v.(string)
		case "direction":
			target.Target.Direction, _ = v.(string)
		case "peer":
			target.Target.Peer, _ = v.(string)
		case "topic":
			target.Target.Topic, _ = v.(string)
		default:
			params[k] = v
		}
	}
	cmd := control.Command{Op: "fault.set", Target: target.Target, Params: params, DurationMs: int(dur.Milliseconds())}
	id := ""
	for i, c := range clients {
		res, err := c.Control(ctx, cmd)
		if err != nil {
			return "", err
		}
		if i == 0 {
			if st, ok := res.State.(map[string]any); ok {
				id, _ = st["id"].(string)
			}
		}
	}
	return id, nil
}

// ClearFaults clears every fault on a node, runtime and block ones alike.
func (e *Engine) ClearFaults(ctx context.Context, depName, nodeID string) error {
	d := e.lookup(depName)
	if d == nil {
		return fmt.Errorf("%w: %s", ErrNotDeployed, depName)
	}
	e.mu.Lock()
	list := append([]activeFault(nil), d.faults[nodeID]...)
	e.mu.Unlock()
	for _, f := range list {
		e.clearFault(d, nodeID, f, false)
	}
	for _, c := range e.clientsFor(d, nodeID) {
		_, _ = c.Control(ctx, control.Command{Op: "fault.clear", Params: map[string]any{"all": true}})
	}
	return nil
}

func (e *Engine) clearFault(d *deployment, nodeID string, f activeFault, expired bool) {
	e.mu.Lock()
	if e.deps[d.name] != d {
		e.mu.Unlock()
		return
	}
	fs := d.faults[nodeID]
	found := false
	for i, g := range fs {
		if g.Kind == f.Kind && g.ID == f.ID && g.Until.Equal(f.Until) {
			d.faults[nodeID] = append(fs[:i], fs[i+1:]...)
			found = true
			break
		}
	}
	e.mu.Unlock()
	if !found {
		return
	}
	ctx := context.Background()
	for _, n := range e.containersFor(d.name, nodeID) {
		switch f.Kind {
		case spec.FaultPause:
			_ = e.rt.Unpause(ctx, n)
		case spec.FaultKill:
			_ = e.rt.Start(ctx, n)
			e.attachClient(ctx, d, n)
		case spec.FaultPartition:
			if f.ID == "" {
				_ = e.rt.Connect(ctx, n, d.network)
			}
		}
	}
	if f.ID != "" && !expired {
		for _, c := range e.clientsFor(d, nodeID) {
			_, _ = c.Control(ctx, control.Command{Op: "fault.clear", Params: map[string]any{"id": f.ID}})
		}
	}
	e.log("info", "fault", fmt.Sprintf("[%s] cleared %s on %s", d.name, f.Kind, nodeID))
	e.mu.Lock()
	remaining := len(d.faults[nodeID])
	e.mu.Unlock()
	if remaining == 0 {
		e.setStatusMessage(d, nodeID, "")
	}
}

// --- status bookkeeping ---------------------------------------------------

func (e *Engine) onContainerState(c runtime.Container) {
	e.mu.Lock()
	ref, ok := e.containers[c.Name]
	d := e.deps[ref.dep]
	if ok && d != nil {
		d.states[c.Name] = c
	}
	e.mu.Unlock()
	if !ok || d == nil {
		return
	}
	if c.OOMKilled {
		e.log("error", "runtime", fmt.Sprintf("[%s] %s was OOM-killed", d.name, c.Name))
	}
	e.recomputeStatus(d, ref.node)
}

// recomputeStatus folds the runtime states and the blocks' health documents
// of a node's replicas into one NodeStatus.
func (e *Engine) recomputeStatus(d *deployment, nodeID string) {
	names := e.containersFor(d.name, nodeID)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ready := 0
	worst := runtime.StateHealthy
	var checks []events.HealthCheck
	msg := ""
	for _, n := range names {
		e.mu.Lock()
		c, cached := d.states[n]
		cl := e.clients[n]
		e.mu.Unlock()
		if !cached {
			var err error
			c, err = e.rt.Inspect(ctx, n)
			if err != nil {
				continue
			}
			e.mu.Lock()
			d.states[n] = c
			e.mu.Unlock()
		}
		state := c.State
		if c.OOMKilled {
			msg = "OOM killed"
		}
		if cl != nil && (state == runtime.StateHealthy || state == runtime.StateStarting) {
			doc, err := cl.Health(ctx)
			switch {
			case err != nil:
				state = runtime.StateStarting
			case doc.Status == health.Failing:
				state = runtime.StateFailed
			case doc.Status == health.Degraded:
				state = "degraded"
			default:
				state = runtime.StateHealthy
			}
			for _, ch := range doc.Checks {
				name := ch.Name
				if len(names) > 1 {
					name = fmt.Sprintf("%d/%s", doc.Replica, ch.Name)
				}
				checks = append(checks, events.HealthCheck{Name: name, Status: string(ch.Status), Detail: ch.Detail})
			}
		}
		if state == runtime.StateHealthy || state == "degraded" {
			ready++
		}
		if stateRank(state) > stateRank(worst) {
			worst = state
		}
	}
	e.mu.Lock()
	st := d.statuses[nodeID]
	if st == nil {
		e.mu.Unlock()
		return
	}
	replicas := st.Replicas
	if msg == "" {
		msg = st.Message
	}
	e.mu.Unlock()
	e.setStatus(d, nodeID, string(worst), replicas, ready, msg, checks)
}

// stateRank orders states by "badness" so a node reports its worst replica.
func stateRank(s runtime.State) int {
	switch s {
	case runtime.StateHealthy:
		return 0
	case "degraded":
		return 1
	case runtime.StateStarting:
		return 2
	case runtime.StatePending:
		return 3
	case runtime.StatePaused:
		return 4
	case runtime.StateStopped:
		return 5
	case runtime.StateFailed:
		return 6
	}
	return 7
}

func (e *Engine) setStatus(d *deployment, nodeID, state string, replicas, ready int, msg string, checks []events.HealthCheck) {
	e.mu.Lock()
	st := &NodeStatus{NodeID: nodeID, State: state, Replicas: replicas, Ready: ready, Message: msg, UpdatedAt: time.Now().UnixMilli(), Checks: checks}
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
		Ready: st.Ready, Message: st.Message, UpdatedAt: st.UpdatedAt, Checks: st.Checks,
	}
}

// pollLoop refreshes deep health and container stats every second.
func (e *Engine) pollLoop(ctx context.Context) {
	tk := time.NewTicker(pollInterval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			e.pollOnce(ctx)
		}
	}
}

func (e *Engine) pollOnce(ctx context.Context) {
	e.mu.Lock()
	type job struct {
		d    *deployment
		node string
	}
	var jobs []job
	var containers []struct {
		d    *deployment
		node string
		name string
	}
	for _, d := range e.deps {
		if d.phase != PhaseRunning && d.phase != PhaseFailed {
			continue
		}
		for _, n := range d.current.Nodes {
			jobs = append(jobs, job{d, n.ID})
			for _, name := range planner.ContainerNames(d.slug, n) {
				if e.containers[name].dep == d.name {
					containers = append(containers, struct {
						d    *deployment
						node string
						name string
					}{d, n.ID, name})
				}
			}
		}
	}
	e.mu.Unlock()

	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(j job) {
			defer wg.Done()
			defer func() { <-sem }()
			e.recomputeStatus(j.d, j.node)
		}(j)
	}
	for _, c := range containers {
		wg.Add(1)
		sem <- struct{}{}
		go func(d *deployment, node, name string) {
			defer wg.Done()
			defer func() { <-sem }()
			st, err := e.rt.Stats(ctx, name)
			if err != nil {
				return
			}
			now := time.Now()
			e.mu.Lock()
			prev, had := d.stats[name]
			d.stats[name] = statSample{Stats: st, at: now}
			e.mu.Unlock()
			p := events.ContainerStatsPayload{Deployment: d.name, NodeID: node, Container: name, Ts: now.UnixMilli(),
				CPUPct: round1(st.CPUPercent), MemUsed: st.MemoryBytes, MemLimit: st.MemoryLimit, Throttled: st.ThrottledPeriods}
			if had {
				secs := now.Sub(prev.at).Seconds()
				if secs > 0 && st.NetRxBytes >= prev.NetRxBytes && st.NetTxBytes >= prev.NetTxBytes {
					p.NetRxBps = float64(st.NetRxBytes-prev.NetRxBytes) / secs
					p.NetTxBps = float64(st.NetTxBytes-prev.NetTxBytes) / secs
				}
			}
			e.bus.Publish(events.ContainerStats, p)
		}(c.d, c.node, c.name)
	}
	wg.Wait()
}

func round1(x float64) float64 { return float64(int(x*10)) / 10 }

// --- adoption ----------------------------------------------------------------

// Adopt rebuilds deployments from containers the runtime still has from a
// previous run of Symphony (same owner id). Containers of deployments no
// record exists for are removed. Deployments the engine already tracks are
// left alone, so it can run again whenever the daemon comes back.
func (e *Engine) Adopt(ctx context.Context) {
	list, err := e.rt.List(ctx)
	if err != nil {
		e.log("warn", "engine", "could not list existing containers: "+err.Error())
		return
	}
	bySlug := map[string][]runtime.Container{}
	for _, c := range list {
		if slug := c.Labels[runtime.LabelDeployment]; slug != "" {
			bySlug[slug] = append(bySlug[slug], c)
		}
	}
	for slug, cs := range bySlug {
		if e.tracks(slug) {
			continue
		}
		rec, err := e.store.load(slug)
		if err != nil {
			e.log("warn", "engine", fmt.Sprintf("removing %d orphaned container(s) of %q (no deployment record)", len(cs), slug))
			for _, c := range cs {
				_ = e.rt.Destroy(ctx, c.Name)
			}
			_ = e.rt.RemoveNetwork(ctx, "symphony-"+slug)
			continue
		}
		d := &deployment{
			name: rec.Name, slug: slug, network: rec.Network, token: rec.Token, phase: PhaseRunning,
			current: rec.Topology, statuses: map[string]*NodeStatus{}, faults: map[string][]activeFault{},
			states: map[string]runtime.Container{}, stats: map[string]statSample{},
		}
		if d.network == "" {
			d.network = planner.NetworkName(rec.Topology)
		}
		e.mu.Lock()
		e.deps[rec.Name] = d
		for _, c := range cs {
			node := c.Labels[runtime.LabelNode]
			e.containers[c.Name] = containerRef{dep: rec.Name, node: node}
			d.states[c.Name] = c
			if c.AdminAddr != "" {
				e.clients[c.Name] = blockclient.New(c.AdminAddr, rec.Token)
			}
		}
		for _, n := range rec.Topology.Nodes {
			d.statuses[n.ID] = &NodeStatus{NodeID: n.ID, State: string(runtime.StatePending), Replicas: n.Replicas, UpdatedAt: time.Now().UnixMilli()}
		}
		e.mu.Unlock()
		for _, n := range rec.Topology.Nodes {
			e.recomputeStatus(d, n.ID)
		}
		e.log("info", "engine", fmt.Sprintf("[%s] adopted %d running container(s) from a previous session", rec.Name, len(cs)))
		e.bus.Publish(events.EnginePhase, events.PhasePayload{Deployment: d.name, Phase: string(PhaseRunning)})
	}
}

// --- helpers ------------------------------------------------------------------

func (e *Engine) anyClient(d *deployment, nodeID string) *blockclient.Client {
	cs := e.clientsFor(d, nodeID)
	if len(cs) == 0 {
		return nil
	}
	return cs[0]
}

func (e *Engine) clientsFor(d *deployment, nodeID string) []*blockclient.Client {
	names := e.containersFor(d.name, nodeID)
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []*blockclient.Client
	for _, n := range names {
		if c := e.clients[n]; c != nil {
			out = append(out, c)
		}
	}
	return out
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

// tracks reports whether a deployment with this slug is known.
func (e *Engine) tracks(slug string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, d := range e.deps {
		if d.slug == slug {
			return true
		}
	}
	return false
}

func (e *Engine) lookup(name string) *deployment {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.deps[name]
}

// begin marks a deployment as busy, creating it when create is set.
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
			token:    NewToken(),
			phase:    PhaseIdle,
			current:  spec.Topology{Version: spec.CurrentVersion, Name: name}.Normalized(),
			statuses: map[string]*NodeStatus{},
			faults:   map[string][]activeFault{},
			states:   map[string]runtime.Container{},
			stats:    map[string]statSample{},
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
			delete(e.clients, name)
		}
	}
	e.mu.Unlock()
	e.store.remove(d.slug)
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
