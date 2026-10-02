// Hot config, limits, raw control commands and logs of a running deployment.

package core

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"symphony/blocks/common/control"
	"symphony/engine/blockclient"
	"symphony/engine/planner"
	"symphony/engine/spec"
)

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
		if _, err := c.Control(ctx, control.ConfigSet(patch)); err != nil {
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
	e.persist(d, e.currentOf(d))
	return nil
}

func (e *Engine) currentOf(d *deployment) spec.Topology {
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
		if err := e.rt.Update(ctx, name, limits(r)); err != nil {
			return err
		}
	}
	e.log("info", "runtime", fmt.Sprintf("[%s] %s: resources cpus=%g memory_mb=%d", d.name, nodeID, r.CPUs, r.MemoryMB))
	e.persist(d, e.currentOf(d))
	return nil
}

// Baseline returns every block of a deployment to what its applied
// topology says, undoing whatever was changed behind the engine's back
// through raw control commands (a test run's work.set, pool.set, behaviour
// edits): the config, the behaviour program and the container limits. With
// state set the blocks also forget what they accumulated (counters,
// idempotency records, the broker's log, the database's rows), so that a
// run does not start from what the previous one left.
func (e *Engine) Baseline(ctx context.Context, depName string, state bool) error {
	d := e.lookup(depName)
	if d == nil {
		return fmt.Errorf("%w: %s", ErrNotDeployed, depName)
	}
	var first error
	keep := func(err error) {
		if err != nil && first == nil {
			first = err
		}
	}
	for _, node := range e.currentOf(d).Nodes {
		for _, name := range e.containersFor(d.name, node.ID) {
			keep(e.rt.Update(ctx, name, limits(node.Resources)))
		}
		cmds := []control.Command{control.ConfigReset()}
		if cfg := mergedConfig(node); len(cfg) > 0 {
			cmds = append(cmds, control.ConfigSet(cfg))
		}
		if node.Kind == spec.KindService {
			cmd, err := control.Behaviour(node.Behaviour)
			if err != nil {
				keep(fmt.Errorf("%s: %w", node.ID, err))
			} else {
				cmds = append(cmds, cmd)
			}
		}
		if state {
			cmds = append(cmds, control.StateReset())
		}
		for _, c := range e.clientsFor(d, node.ID) {
			for _, cmd := range cmds {
				if _, err := c.Control(ctx, cmd); err != nil {
					keep(fmt.Errorf("%s: %w", node.ID, err))
				}
			}
		}
	}
	e.log("info", "control", fmt.Sprintf("[%s] blocks returned to the applied topology", d.name))
	return first
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
	switch cmd.Op {
	case control.OpLoadSet:
		// only a load without an end is state worth restoring; a timed one
		// replaces it and then stops by itself
		if cmd.DurationMs > 0 || positive(cmd.Params["duration_ms"]) {
			e.setLoad(d, nodeID, nil)
		} else {
			e.setLoad(d, nodeID, cmd.Params)
		}
	case control.OpLoadStop:
		e.setLoad(d, nodeID, nil)
	}
	if !control.ReadOnly(cmd.Op) {
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
