// Plan, apply and teardown: turning a plan into runtime calls.

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"symphony/blocks/common/control"
	"symphony/blocks/common/health"
	"symphony/engine/blockclient"
	"symphony/engine/events"
	"symphony/engine/planner"
	"symphony/engine/runtime"
	"symphony/engine/spec"
)

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
	// stored before anything is created: the containers are never without
	// a record, and an apply cut short is resumed from the desired state
	e.persist(d, desired)
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
	e.persist(d, desired)
	e.log("info", "engine", fmt.Sprintf("[%s] apply complete in %s", d.name, time.Since(start).Round(time.Millisecond)))
	if len(desired.Nodes) == 0 {
		// applied down to nothing: the same end as a teardown
		if err := e.rt.RemoveNetwork(ctx, d.network); err != nil {
			e.log("warn", "runtime", fmt.Sprintf("[%s] %v", d.name, err))
		}
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
	// what the plan did not know of: containers an apply created before it
	// failed, which never became part of the applied topology
	if err := e.sweep(ctx, d); err != nil {
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

// sweep destroys every container the runtime still has of a deployment.
func (e *Engine) sweep(ctx context.Context, d *deployment) error {
	list, err := e.rt.List(ctx)
	if err != nil {
		return err
	}
	for _, c := range list {
		if c.Labels[runtime.LabelDeployment] != d.slug {
			continue
		}
		if err := e.rt.Destroy(ctx, c.Name); err != nil && !errors.Is(err, runtime.ErrNotFound) {
			return err
		}
		e.mu.Lock()
		delete(e.containers, c.Name)
		delete(e.clients, c.Name)
		delete(d.states, c.Name)
		delete(d.stats, c.Name)
		e.mu.Unlock()
		e.log("info", "runtime", fmt.Sprintf("[%s] destroyed %s (left over)", d.name, c.Name))
	}
	return nil
}

// create makes one container. A container of this deployment that already
// has the name is one an earlier apply created before it failed: the
// applied topology never included it, so the plan did not destroy it. It is
// replaced, which is what lets a failed apply be tried again.
func (e *Engine) create(ctx context.Context, d *deployment, cspec runtime.ContainerSpec) error {
	_, err := e.rt.Create(ctx, cspec)
	if !errors.Is(err, runtime.ErrExists) {
		return err
	}
	list, lerr := e.rt.List(ctx)
	if lerr != nil {
		return err
	}
	for _, c := range list {
		if c.Name != cspec.Name || c.Labels[runtime.LabelDeployment] != d.slug {
			continue
		}
		e.log("warn", "runtime", fmt.Sprintf("[%s] replacing %s, left over from an apply that did not finish", d.name, c.Name))
		if derr := e.rt.Destroy(ctx, c.Name); derr != nil && !errors.Is(derr, runtime.ErrNotFound) {
			return derr
		}
		_, err = e.rt.Create(ctx, cspec)
		return err
	}
	// not one of this installation's: leave it alone
	return fmt.Errorf("%w (and Symphony did not create it for this deployment)", err)
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
				Resources:   limits(node.Resources),
				AdminPort:   planner.AdminPort,
				DataPort:    planner.DataPort,
				Healthcheck: spec.IsBlockImage(node.Image),
			}
			e.mu.Lock()
			e.containers[name] = containerRef{dep: d.name, node: node.ID}
			e.mu.Unlock()
			if err := e.create(ctx, d, cspec); err != nil {
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
		topic, _ := op.Params["topic"].(string)
		if c := e.anyClient(d, broker); c != nil {
			if _, err := c.Control(ctx, control.TopicCreate(topic)); err != nil {
				return err
			}
		}
		e.log("info", "broker", fmt.Sprintf("[%s] ensured topic %v on %v", d.name, op.Params["topic"], op.Params["broker"]))
		return nil

	case planner.OpControl:
		return e.pushConfig(ctx, d, op.Target, op.Params)

	case planner.OpConfigure:
		// the program of the node as desired; none returns the block to
		// the one it derives from its links
		node, _ := desired.NodeByID(op.Target)
		cmd, err := control.Behaviour(node.Behaviour)
		if err != nil {
			return err
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
			if err := e.rt.Update(ctx, name, limits(node.Resources)); err != nil {
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
		stored := len(d.faults[op.Target]) > 0
		delete(d.faults, op.Target)
		e.mu.Unlock()
		if stored {
			// the faults went with the containers
			e.persistMu.Lock()
			rev, err := e.store.ClearFaults(d.slug, op.Target)
			e.stored(d, rev, err)
			e.persistMu.Unlock()
		}
		e.bus.Publish(events.NodeStatus, events.NodeStatusPayload{
			Deployment: d.name, NodeID: op.Target, State: "removed", UpdatedAt: time.Now().UnixMilli(),
		})
		return nil
	}
	return fmt.Errorf("unknown op kind %q", op.Kind)
}

// limits are a node's resources as the runtime takes them.
func limits(r spec.Resources) runtime.Resources {
	return runtime.Resources{CPUs: r.CPUs, MemoryMB: r.MemoryMB, PidsMax: r.PidsMax}
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
	// the same address keeps its client: the collector's deltas hang on it
	if cur := e.clients[name]; cur == nil || cur.Addr() != c.AdminAddr {
		e.clients[name] = blockclient.New(c.AdminAddr, d.token)
	}
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
