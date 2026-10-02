// Stored deployments: picking them up again on start, following what other
// processes write, and writing this engine's own changes.

package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"symphony/blocks/common/control"
	"symphony/engine/blockclient"
	"symphony/engine/events"
	"symphony/engine/planner"
	"symphony/engine/runtime"
	"symphony/engine/spec"
	"symphony/engine/store"
)

// Restore picks up every deployment the store holds, the way the app does on
// start and whenever the Docker daemon comes back: running ones are attached
// as they are, ones whose containers are gone or stopped are recreated in
// the background (see Reopen), and containers of this installation that no
// deployment owns any more are removed.
func (e *Engine) Restore(ctx context.Context) {
	broken, err := e.Attach(ctx)
	if err != nil {
		e.log("warn", "engine", "could not read the stored deployments: "+err.Error())
		return
	}
	e.CollectOrphans(ctx)
	e.ResumeFaults(broken)
	for _, name := range broken {
		go func(name string) {
			if err := e.Reopen(ctx, name); err != nil {
				e.log("error", "engine", fmt.Sprintf("[%s] reopen failed: %v", name, err))
			}
		}(name)
	}
}

// Attach makes every stored deployment live in this engine from its record
// and the containers the runtime still has of it, creating and removing
// nothing. It returns the ones that need Reopen: an apply or a teardown was
// cut short, or a container a running deployment needs is gone or stopped.
func (e *Engine) Attach(ctx context.Context) ([]string, error) {
	recs, err := e.store.Deployments()
	if err != nil || len(recs) == 0 {
		return nil, err
	}
	bySlug, err := e.containersBySlug(ctx)
	if err != nil {
		return nil, err
	}
	var broken []string
	for _, rec := range recs {
		cs := bySlug[rec.Slug]
		known := e.tracks(rec.Slug)
		d := e.attach(rec, cs)
		if d == nil {
			continue // busy here: this engine knows better than the record
		}
		// containers a stored fault holds down or paused are as they should
		// be: the fault says when that ends, not this attach
		down, paused := e.heldByFaults(d)
		switch Phase(rec.Phase) {
		case PhaseApplying, PhaseValidating, PhasePlanning, PhaseTearingDown:
			broken = append(broken, rec.Name)
		case PhaseRunning:
			if !intact(rec, cs, down) {
				broken = append(broken, rec.Name)
			}
		}
		if !known {
			e.unpause(ctx, rec.Name, cs, paused)
		}
	}
	return broken, nil
}

// CollectOrphans removes the containers (and networks) of this installation
// that belong to no stored deployment. Records are written before an apply
// creates anything, so a container without one is left over from a crash.
func (e *Engine) CollectOrphans(ctx context.Context) {
	bySlug, err := e.containersBySlug(ctx)
	if err != nil {
		return
	}
	for slug, cs := range bySlug {
		if _, ok, err := e.store.Deployment(slug); err != nil || ok || e.tracks(slug) {
			continue
		}
		e.log("warn", "engine", fmt.Sprintf("removing %d orphaned container(s) of %q (no stored deployment)", len(cs), slug))
		for _, c := range cs {
			_ = e.rt.Destroy(ctx, c.Name)
		}
		_ = e.rt.RemoveNetwork(ctx, "symphony-"+slug)
	}
}

// Reopen brings a stored deployment back to what its record describes: an
// interrupted teardown is finished, anything else is recreated from the
// stored topology, and the load its nodes were driving is started again.
func (e *Engine) Reopen(ctx context.Context, name string) error {
	d := e.lookup(name)
	if d == nil {
		return fmt.Errorf("%w: %s", ErrNotDeployed, name)
	}
	e.mu.Lock()
	phase, topo, load := d.phase, d.current.Clone(), cloneLoad(d.load)
	e.mu.Unlock()
	if phase == PhaseTearingDown {
		e.log("info", "engine", fmt.Sprintf("[%s] finishing the teardown a previous session started", name))
		return e.Teardown(ctx, name)
	}
	e.log("info", "engine", fmt.Sprintf("[%s] reopening: recreating its containers from the stored state", name))
	if err := e.clear(ctx, d); err != nil {
		return err
	}
	if err := e.Apply(ctx, topo); err != nil {
		return err
	}
	for node, params := range load {
		if _, err := e.Control(ctx, name, node, control.LoadSet(params)); err != nil {
			e.log("warn", "engine", fmt.Sprintf("[%s] %s: could not restart the load: %v", name, node, err))
		}
	}
	return nil
}

// clear destroys every container of a deployment and leaves it empty, so
// the next Apply creates everything afresh.
func (e *Engine) clear(ctx context.Context, d *deployment) error {
	if _, err := e.begin(d.name, false); err != nil {
		return err
	}
	defer e.end(d)
	e.mu.Lock()
	var names []string
	for name, ref := range e.containers {
		if ref.dep == d.name {
			names = append(names, name)
		}
	}
	e.mu.Unlock()
	for _, name := range names {
		if err := e.rt.Destroy(ctx, name); err != nil && !errors.Is(err, runtime.ErrNotFound) {
			return err
		}
		e.mu.Lock()
		delete(e.containers, name)
		delete(e.clients, name)
		e.mu.Unlock()
	}
	e.mu.Lock()
	d.current = spec.Topology{Version: d.current.Version, Name: d.name}.Normalized()
	d.statuses = map[string]*NodeStatus{}
	d.faults = map[string][]activeFault{}
	d.states = map[string]runtime.Container{}
	d.stats = map[string]statSample{}
	e.mu.Unlock()
	e.persistMu.Lock()
	rev, err := e.store.ClearFaults(d.slug, "")
	e.stored(d, rev, err)
	e.persistMu.Unlock()
	return nil
}

// attach builds (or refreshes in place) the in-memory deployment of a
// record. It returns nil, touching nothing, while this engine is applying
// or tearing that deployment down itself.
func (e *Engine) attach(rec store.Deployment, cs []runtime.Container) *deployment {
	stored, err := e.store.Faults(rec.Slug)
	if err != nil {
		e.log("warn", "store", fmt.Sprintf("[%s] %v", rec.Name, err))
	}
	e.mu.Lock()
	d := e.deps[rec.Name]
	if d != nil && d.applying {
		e.mu.Unlock()
		return nil
	}
	if d == nil {
		d = &deployment{name: rec.Name, faults: map[string][]activeFault{}, stats: map[string]statSample{}}
		e.deps[rec.Name] = d
	}
	d.slug, d.token, d.network, d.rev = rec.Slug, rec.Token, rec.Network, rec.Rev
	d.phase = Phase(rec.Phase)
	d.current = rec.Topology
	d.load = rec.Load
	if err == nil {
		d.faults = mergeFaults(d.faults, stored)
	}
	if d.network == "" {
		d.network = planner.NetworkName(rec.Topology)
	}
	for name, ref := range e.containers {
		if ref.dep == d.name {
			delete(e.containers, name)
			delete(e.clients, name)
		}
	}
	d.states = map[string]runtime.Container{}
	for _, c := range cs {
		e.containers[c.Name] = containerRef{dep: d.name, node: c.Labels[runtime.LabelNode]}
		d.states[c.Name] = c
		if c.AdminAddr != "" {
			e.clients[c.Name] = blockclient.New(c.AdminAddr, d.token)
		}
	}
	var removed []string
	for id := range d.statuses {
		if _, ok := rec.Topology.NodeByID(id); !ok {
			removed = append(removed, id)
		}
	}
	d.statuses = map[string]*NodeStatus{}
	for _, n := range rec.Topology.Nodes {
		d.statuses[n.ID] = &NodeStatus{NodeID: n.ID, State: string(runtime.StatePending), Replicas: n.Replicas, UpdatedAt: time.Now().UnixMilli()}
	}
	e.mu.Unlock()

	for _, id := range removed {
		e.bus.Publish(events.NodeStatus, events.NodeStatusPayload{Deployment: d.name, NodeID: id, State: "removed", UpdatedAt: time.Now().UnixMilli()})
	}
	e.forgetExpired(d)
	for _, n := range rec.Topology.Nodes {
		e.recomputeStatus(d, n.ID)
	}
	e.bus.Publish(events.EnginePhase, events.PhasePayload{Deployment: d.name, Phase: string(d.phase)})
	return d
}

// intact reports whether every replica a stored deployment describes has a
// container whose process is up.
// Nodes in down are held stopped by a fault; their containers only have to
// exist.
func intact(rec store.Deployment, cs []runtime.Container, down map[string]bool) bool {
	up := make(map[string]bool, len(cs))
	exists := make(map[string]bool, len(cs))
	for _, c := range cs {
		up[c.Name] = c.Running
		exists[c.Name] = true
	}
	for _, n := range rec.Topology.Nodes {
		for _, name := range planner.ContainerNames(rec.Slug, n) {
			if !up[name] && !(down[n.ID] && exists[name]) {
				return false
			}
		}
	}
	return true
}

// unpause resumes containers that are paused with no fault on record to say
// so (one from before faults were stored, or paused by hand).
func (e *Engine) unpause(ctx context.Context, name string, cs []runtime.Container, held map[string]bool) {
	for _, c := range cs {
		if c.State == runtime.StatePaused && !held[c.Labels[runtime.LabelNode]] {
			if err := e.rt.Unpause(ctx, c.Name); err == nil {
				e.log("info", "engine", fmt.Sprintf("[%s] resumed %s, left paused by a previous session", name, c.Name))
			}
		}
	}
}

// containersBySlug lists this installation's containers per deployment.
func (e *Engine) containersBySlug(ctx context.Context) (map[string][]runtime.Container, error) {
	list, err := e.rt.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	out := map[string][]runtime.Container{}
	for _, c := range list {
		if slug := c.Labels[runtime.LabelDeployment]; slug != "" {
			out[slug] = append(out[slug], c)
		}
	}
	return out, nil
}

// syncStore follows what other processes write to the store: deployments
// they applied or changed are attached, ones they tore down are forgotten.
// It runs every few seconds, so it compares revisions first and reads only
// the deployments that changed.
func (e *Engine) syncStore(ctx context.Context) {
	e.persistMu.Lock()
	revs, err := e.store.Revisions()
	if err != nil {
		e.persistMu.Unlock()
		return
	}
	var changed []string
	var gone []*deployment
	e.mu.Lock()
	for slug, r := range revs {
		d := e.deps[r.Name]
		if d == nil || (d.rev != r.Rev && !d.applying) {
			changed = append(changed, slug)
		}
	}
	for _, d := range e.deps {
		if _, ok := revs[d.slug]; !ok && d.rev > 0 && !d.applying {
			gone = append(gone, d)
		}
	}
	e.mu.Unlock()
	e.persistMu.Unlock()

	for _, d := range gone {
		e.log("info", "engine", fmt.Sprintf("[%s] torn down by another Symphony process", d.name))
		e.forget(d)
	}
	if len(changed) == 0 {
		return
	}
	bySlug, err := e.containersBySlug(ctx)
	if err != nil {
		return
	}
	for _, slug := range changed {
		rec, ok, err := e.store.Deployment(slug)
		if err != nil || !ok {
			continue
		}
		if d := e.attach(rec, bySlug[rec.Slug]); d != nil {
			e.resumeFaults(d)
			e.log("info", "engine", fmt.Sprintf("[%s] picked up a change another Symphony process made", rec.Name))
			e.bus.Publish(events.DeploymentSync, events.PhasePayload{Deployment: rec.Name, Phase: rec.Phase})
		}
	}
}

// persist writes a deployment to the store with the given topology: the one
// being applied while an apply runs, the running one otherwise.
func (e *Engine) persist(d *deployment, topo spec.Topology) {
	e.persistMu.Lock()
	defer e.persistMu.Unlock()
	e.mu.Lock()
	rec := store.Deployment{Name: d.name, Slug: d.slug, Token: d.token, Network: d.network, Phase: string(d.phase), Topology: topo, Load: cloneLoad(d.load)}
	e.mu.Unlock()
	if rec.Network == "" {
		rec.Network = planner.NetworkName(topo)
	}
	rev, err := e.store.Save(rec)
	e.stored(d, rev, err)
}

func (e *Engine) persistPhase(d *deployment, p Phase) {
	e.persistMu.Lock()
	defer e.persistMu.Unlock()
	rev, err := e.store.SetPhase(d.slug, string(p))
	e.stored(d, rev, err)
}

// setLoad remembers the load a node drives (nil: none).
func (e *Engine) setLoad(d *deployment, nodeID string, params map[string]any) {
	e.persistMu.Lock()
	defer e.persistMu.Unlock()
	e.mu.Lock()
	if params == nil {
		delete(d.load, nodeID)
	} else {
		if d.load == nil {
			d.load = map[string]map[string]any{}
		}
		d.load[nodeID] = params
	}
	load := cloneLoad(d.load)
	e.mu.Unlock()
	rev, err := e.store.SetLoad(d.slug, load)
	e.stored(d, rev, err)
}

// stored records the revision a write produced. Callers hold persistMu.
func (e *Engine) stored(d *deployment, rev int64, err error) {
	if err != nil {
		e.log("warn", "store", fmt.Sprintf("[%s] %v", d.name, err))
		return
	}
	if rev > 0 {
		e.mu.Lock()
		d.rev = rev
		e.mu.Unlock()
	}
}

func cloneLoad(in map[string]map[string]any) map[string]map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func positive(v any) bool {
	f, ok := v.(float64)
	if !ok {
		if i, isInt := v.(int); isInt {
			f, ok = float64(i), true
		}
	}
	return ok && f > 0
}
