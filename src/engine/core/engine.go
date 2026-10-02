// Package core drives topologies from desired state to running state:
// validate -> diff -> plan -> execute against a Runtime, emitting progress
// on the event bus. Several topologies can be applied at once; each one is a
// deployment with its own network, containers, statuses and faults, keyed by
// the topology's name. The engine also owns the hot-config, fault-injection
// and control entry points used by the UI, talking to the blocks' admin
// ports through blockclient. Every deployment's state is written to the
// store as it changes, so a later Symphony (or the CLI next to it) can pick
// the deployments up again.
package core

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"symphony/engine/blockclient"
	"symphony/engine/events"
	"symphony/engine/images"
	"symphony/engine/planner"
	"symphony/engine/runtime"
	"symphony/engine/spec"
	"symphony/engine/store"
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
	ErrNameTaken      = errors.New("engine: another deployment already uses this name's containers and network")
)

const (
	healthyTimeout = 90 * time.Second // image builds happen before, so this is just startup
	healthyPoll    = 150 * time.Millisecond
	// how long the blocks run a chaos.cpu / chaos.stall given no duration
	defaultBurnMs  = 10000
	defaultStallMs = 5000
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
	// open-ended load.set params per node, restarted when reopened
	load map[string]map[string]any
	// the store revision this engine last wrote or read; 0 = not stored
	rev int64
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
	// Store keeps the deployments across runs; nil keeps nothing.
	Store *store.Store
	// FollowStore makes the engine pick up deployments another process
	// (the CLI) applies, changes or tears down through the same store.
	FollowStore bool
}

// Engine is safe for concurrent use; Wails invokes bound methods from
// multiple goroutines, and different deployments may apply at the same time.
type Engine struct {
	rt     runtime.Runtime
	bus    *events.Bus
	images *images.Ensurer
	store  *store.Store
	follow bool

	// persistMu orders store writes with the revision bookkeeping, so the
	// store follower never mistakes this engine's own write for someone
	// else's.
	persistMu sync.Mutex

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
		follow:     opts.FollowStore,
		deps:       map[string]*deployment{},
		containers: map[string]containerRef{},
		clients:    map[string]*blockclient.Client{},
	}
	if e.images == nil {
		e.images = images.NewEnsurer(nil)
	}
	e.rt.OnStateChange(e.onContainerState)
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
		// names that differ only in case or punctuation share a slug, and
		// with it the network, the container names and the store record
		slug := spec.Slug(name)
		for _, other := range e.deps {
			if other.slug == slug {
				return nil, fmt.Errorf("%w: %q and %q", ErrNameTaken, name, other.name)
			}
		}
		if rec, ok, err := e.store.Deployment(slug); err == nil && ok && rec.Name != name {
			return nil, fmt.Errorf("%w: %q and %q", ErrNameTaken, name, rec.Name)
		}
		d = &deployment{
			name:     name,
			slug:     spec.Slug(name),
			token:    store.RandomID(16),
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

// remove forgets a deployment that has nothing running any more, here and
// in the store.
func (e *Engine) remove(d *deployment) {
	e.persistMu.Lock()
	if err := e.store.Delete(d.slug); err != nil {
		e.log("warn", "store", fmt.Sprintf("[%s] %v", d.name, err))
	}
	e.persistMu.Unlock()
	e.forget(d)
}

// forget drops a deployment from memory only.
func (e *Engine) forget(d *deployment) {
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
	e.bus.Publish(events.EnginePhase, events.PhasePayload{Deployment: d.name, Phase: string(PhaseGone)})
}

func (e *Engine) setPhase(d *deployment, p Phase, msg string) {
	e.mu.Lock()
	d.phase = p
	e.mu.Unlock()
	e.persistPhase(d, p)
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
