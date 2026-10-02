// Fault injection: what is injected where, how it is undone, and the record
// of it that lets another session undo it.

package core

import (
	"context"
	"fmt"
	"time"

	"symphony/blocks/common/control"
	"symphony/blocks/common/faults"
	"symphony/engine/blockclient"
	"symphony/engine/runtime"
	"symphony/engine/spec"
	"symphony/engine/store"
)

type activeFault struct {
	spec.FaultSpec
	ID    string // block-side fault id, "" for runtime faults
	Until time.Time
	// rowID is the fault's id in the store, 0 when it is not stored
	rowID int64
	// armed: this engine has a timer running that ends the fault at Until
	armed bool
}

// same reports whether two records are the same fault.
func (f activeFault) same(g activeFault) bool {
	if f.rowID != 0 || g.rowID != 0 {
		return f.rowID == g.rowID
	}
	return f.Kind == g.Kind && f.ID == g.ID && f.Until.Equal(g.Until)
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
	if fault.DurationMs <= 0 {
		// a burn and a stall always end by themselves: say when, so the
		// engine's record of the fault ends with them
		switch fault.Kind {
		case spec.FaultCPU:
			fault.DurationMs = defaultBurnMs
		case spec.FaultStall:
			fault.DurationMs = defaultStallMs
		}
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
		if fault.Selective() && len(clients) > 0 {
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
			cmd := control.Chaos(fault.Kind.ChaosOp(), fault.Params, fault.DurationMs)
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

	// stored, so that a fault outlives this process: whichever Symphony is
	// around when it is due ends it (see resumeFaults)
	af.rowID = e.recordFault(d, nodeID, af)
	af.armed = dur > 0
	e.mu.Lock()
	d.faults[nodeID] = append(d.faults[nodeID], af)
	e.mu.Unlock()
	e.log("warn", "fault", fmt.Sprintf("[%s] injected %s into %s (%dms) %v", d.name, fault.Kind, nodeID, fault.DurationMs, fault.Params))
	e.setStatusMessage(d, nodeID, "fault: "+string(fault.Kind))

	if dur > 0 {
		time.AfterFunc(dur, func() { e.clearFault(d, nodeID, af, true) })
	}
	return nil
}

// blockFault translates a FaultSpec into a fault.set command and returns
// the block-side fault id (of the first replica).
func (e *Engine) blockFault(ctx context.Context, clients []*blockclient.Client, fault spec.FaultSpec, dur time.Duration) (string, error) {
	cmd := control.FaultSet(faults.Kind(fault.Kind), fault.Params, dur)
	id := ""
	for i, c := range clients {
		res, err := c.Control(ctx, cmd)
		if err != nil {
			return "", err
		}
		if i == 0 {
			var set faults.Fault
			if err := res.Decode(&set); err != nil {
				return "", err
			}
			id = set.ID
		}
	}
	return id, nil
}

// ClearFaults clears every fault on a node, runtime and block ones alike,
// including ones another process injected (the CLI, a previous session):
// whatever state a container was left in by a fault is undone too.
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
	e.persistMu.Lock()
	rev, err := e.store.ClearFaults(d.slug, nodeID)
	e.stored(d, rev, err)
	e.persistMu.Unlock()
	for _, name := range e.containersFor(d.name, nodeID) {
		c, err := e.rt.Inspect(ctx, name)
		if err != nil {
			continue
		}
		switch {
		case c.State == runtime.StatePaused:
			_ = e.rt.Unpause(ctx, name)
		case !c.Running:
			if e.rt.Start(ctx, name) == nil {
				e.attachClient(ctx, d, name)
			}
		}
		e.reconnect(ctx, d, name) // changes nothing unless partitioned
	}
	for _, c := range e.clientsFor(d, nodeID) {
		_, _ = c.Control(ctx, control.FaultClearAll())
		_, _ = c.Control(ctx, control.ChaosClear())
	}
	return nil
}

// reconnect puts a container back on its deployment's network after a
// whole-node partition. Docker publishes the admin port anew when it does,
// possibly on another host port, so the admin client is rebuilt.
func (e *Engine) reconnect(ctx context.Context, d *deployment, name string) {
	if err := e.rt.Connect(ctx, name, d.network); err != nil {
		e.log("warn", "runtime", fmt.Sprintf("[%s] %v", d.name, err))
	}
	e.attachClient(ctx, d, name)
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
		if g.same(f) {
			d.faults[nodeID] = append(fs[:i:i], fs[i+1:]...)
			found = true
			break
		}
	}
	e.mu.Unlock()
	if !found {
		return
	}
	if f.rowID != 0 {
		e.persistMu.Lock()
		rev, err := e.store.RemoveFault(d.slug, f.rowID)
		e.stored(d, rev, err)
		e.persistMu.Unlock()
	}
	ctx := context.Background()
	for _, n := range e.containersFor(d.name, nodeID) {
		switch f.Kind {
		case spec.FaultPause:
			_ = e.rt.Unpause(ctx, n)
		case spec.FaultKill, spec.FaultCrash:
			// the process is gone either way; the fault ends when it is back
			_ = e.rt.Start(ctx, n)
			e.attachClient(ctx, d, n)
		case spec.FaultPartition:
			if f.ID == "" {
				e.reconnect(ctx, d, n)
			}
		}
	}
	if !expired {
		for _, c := range e.clientsFor(d, nodeID) {
			switch {
			case f.ID != "":
				_, _ = c.Control(ctx, control.FaultClear(f.ID))
			case f.Kind == spec.FaultCPU, f.Kind == spec.FaultMemory, f.Kind == spec.FaultStall:
				_, _ = c.Control(ctx, control.ChaosClear())
			}
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

// mergeFaults is a deployment's faults as the store has them: records this
// engine already holds keep their state (the timer that ends them), ones
// another process cleared are dropped, and ones it injected are added.
// Faults that were never stored stay.
func mergeFaults(have map[string][]activeFault, stored []store.Fault) map[string][]activeFault {
	byRow := map[int64]activeFault{}
	out := map[string][]activeFault{}
	for node, fs := range have {
		for _, f := range fs {
			if f.rowID == 0 {
				out[node] = append(out[node], f)
			} else {
				byRow[f.rowID] = f
			}
		}
	}
	for _, sf := range stored {
		f, ok := byRow[sf.ID]
		if !ok {
			f = activeFault{FaultSpec: spec.FaultSpec{Kind: spec.FaultKind(sf.Kind), Params: sf.Params}, ID: sf.BlockID, Until: sf.Until, rowID: sf.ID}
		}
		out[sf.Node] = append(out[sf.Node], f)
	}
	return out
}

// heldByFaults lists the nodes whose containers a fault keeps stopped
// (kill, crash) and the ones it keeps paused.
func (e *Engine) heldByFaults(d *deployment) (down, paused map[string]bool) {
	down, paused = map[string]bool{}, map[string]bool{}
	e.mu.Lock()
	defer e.mu.Unlock()
	for node, fs := range d.faults {
		for _, f := range fs {
			switch f.Kind {
			case spec.FaultKill, spec.FaultCrash:
				down[node] = true
			case spec.FaultPause:
				paused[node] = true
			}
		}
	}
	return down, paused
}

// ResumeFaults takes over the faults of every deployment but the ones named
// (those are about to be recreated, faults and all): see resumeFaults. It is
// part of restoring; a process that only attaches leaves other sessions'
// faults to them.
func (e *Engine) ResumeFaults(except []string) {
	skip := map[string]bool{}
	for _, name := range except {
		skip[name] = true
	}
	e.mu.Lock()
	var deps []*deployment
	for _, d := range e.deps {
		if !skip[d.name] {
			deps = append(deps, d)
		}
	}
	e.mu.Unlock()
	for _, d := range deps {
		e.resumeFaults(d)
	}
}

// forgetExpired drops the faults that ended by themselves inside their
// block and need nothing undone: only the record is left, from a process
// that was gone before its timer fired (a CLI command returns at once).
// Any session may do this; it touches no container.
func (e *Engine) forgetExpired(d *deployment) {
	now := time.Now()
	var rows []int64
	e.mu.Lock()
	for node, fs := range d.faults {
		kept := fs[:0:0]
		for _, f := range fs {
			if !f.armed && !f.Until.IsZero() && now.After(f.Until) && !f.NeedsRevert() {
				if f.rowID != 0 {
					rows = append(rows, f.rowID)
				}
				continue
			}
			kept = append(kept, f)
		}
		if len(kept) == 0 {
			delete(d.faults, node)
		} else {
			d.faults[node] = kept
		}
	}
	e.mu.Unlock()
	if len(rows) == 0 {
		return
	}
	e.persistMu.Lock()
	defer e.persistMu.Unlock()
	for _, id := range rows {
		rev, err := e.store.RemoveFault(d.slug, id)
		e.stored(d, rev, err)
	}
}

// resumeFaults takes over the faults of a deployment that this engine has
// no timer for: another process injected them, or this one in an earlier
// life. One that is overdue is ended now, one still running gets its timer.
// Faults without an end stay until they are cleared.
func (e *Engine) resumeFaults(d *deployment) {
	type item struct {
		node string
		f    activeFault
	}
	var due []item
	e.mu.Lock()
	for node, fs := range d.faults {
		for i := range fs {
			if fs[i].armed || fs[i].Until.IsZero() {
				continue
			}
			fs[i].armed = true
			due = append(due, item{node, fs[i]})
		}
	}
	e.mu.Unlock()
	for _, it := range due {
		it := it
		left := time.Until(it.f.Until)
		if left <= 0 {
			e.log("info", "fault", fmt.Sprintf("[%s] %s on %s is overdue, ending it", d.name, it.f.Kind, it.node))
			e.clearFault(d, it.node, it.f, true)
			continue
		}
		time.AfterFunc(left, func() { e.clearFault(d, it.node, it.f, true) })
	}
}

// recordFault stores a fault and returns its row id (0 when nothing is
// stored).
func (e *Engine) recordFault(d *deployment, nodeID string, f activeFault) int64 {
	e.persistMu.Lock()
	defer e.persistMu.Unlock()
	id, rev, err := e.store.AddFault(d.slug, store.Fault{Node: nodeID, Kind: string(f.Kind), BlockID: f.ID, Params: f.Params, Until: f.Until})
	e.stored(d, rev, err)
	return id
}
