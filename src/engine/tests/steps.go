// Timeline steps: which node or edge a step means, and what it does to it.

package tests

import (
	"context"
	"errors"
	"fmt"
	"math"

	"symphony/blocks/common/control"
	"symphony/engine/spec"
)

func nodeOfKind(t spec.Topology, k spec.NodeKind) string {
	for _, n := range t.Nodes {
		if n.Kind == k {
			return n.ID
		}
	}
	return ""
}

// target resolves a step's node / edge.
func (r *Runner) target(t spec.Topology, st Step) (node string, edge string, err error) {
	switch {
	case st.Edge == "proxied":
		for _, e := range t.Edges {
			if n, ok := t.NodeByID(e.From); ok && n.Kind == spec.KindProxy {
				return e.From, e.ID, nil
			}
		}
		for _, e := range t.Edges {
			if e.Kind == spec.EdgeCalls {
				if n, ok := t.NodeByID(e.From); ok && n.Kind != spec.KindInfra {
					return e.From, e.ID, nil
				}
			}
		}
		for _, e := range t.Edges {
			if e.Kind == spec.EdgeSubscribes {
				return e.From, e.ID, nil
			}
		}
		return "", "", errors.New("no edge to target")
	case st.Edge != "":
		for _, e := range t.Edges {
			if e.ID == st.Edge {
				return e.From, e.ID, nil
			}
		}
		return "", "", fmt.Errorf("unknown edge %q", st.Edge)
	case st.Node != "":
		if _, ok := t.NodeByID(st.Node); !ok {
			return "", "", fmt.Errorf("unknown node %q", st.Node)
		}
		return st.Node, "", nil
	case st.Kind != "":
		switch st.Kind {
		case "central":
			// the thing everything depends on: the broker, else the first service
			if n := nodeOfKind(t, spec.KindBroker); n != "" {
				return n, "", nil
			}
			return nodeOfKind(t, spec.KindService), "", nil
		case "participant":
			// a service that is called by another service (not the entry point)
			for _, e := range t.Edges {
				if from, ok := t.NodeByID(e.From); ok && from.Kind != spec.KindInfra {
					if to, ok := t.NodeByID(e.To); ok && to.Kind == spec.KindService {
						return to.ID, "", nil
					}
				}
			}
			return nodeOfKind(t, spec.KindService), "", nil
		case "consumer":
			for _, e := range t.Edges {
				if e.Kind == spec.EdgeSubscribes {
					if to, ok := t.NodeByID(e.To); ok && to.Kind == spec.KindService {
						return to.ID, "", nil
					}
				}
			}
			return nodeOfKind(t, spec.KindService), "", nil
		}
		if n := nodeOfKind(t, spec.NodeKind(st.Kind)); n != "" {
			return n, "", nil
		}
		return "", "", fmt.Errorf("no node of kind %q", st.Kind)
	}
	return "", "", nil
}

// step executes one timeline entry.
func (r *Runner) step(ctx context.Context, a *activeRun, st Step) error {
	run := a.run
	args, _ := resolve(st.Args, run.Params).(map[string]any)
	if args == nil {
		args = map[string]any{}
	}
	node, edge, err := r.target(run.Topology, st)
	if err != nil && st.Do != "phase" && st.Do != "end" && st.Do != "teardown" && st.Do != "apply" && st.Do != "load.set" && st.Do != "load.stop" {
		return err
	}
	gw := nodeOfKind(run.Topology, spec.KindInfra)
	switch st.Do {
	case "phase", "end", "wait":
		return nil
	case "load.set":
		r.note(a, "load.set %v", args)
		_, err := r.eng.Control(ctx, run.Template, gw, control.LoadSet(args))
		return err
	case "load.stop":
		r.note(a, "load.stop")
		_, err := r.eng.Control(ctx, run.Template, gw, control.LoadStop())
		return err
	case "fault.set":
		kind, _ := args["kind"].(string)
		params := map[string]any{}
		dur := 0
		for k, v := range args {
			switch k {
			case "kind":
			case "duration_ms":
				if f, ok := toFloat(v); ok {
					dur = int(f)
				}
			case "duration_s":
				if f, ok := toFloat(v); ok {
					dur = int(f * 1000)
				}
			default:
				params[k] = v
			}
		}
		if edge != "" {
			params["edge"] = edge
			params["direction"] = "out"
		}
		r.note(a, "fault %s on %s %v for %dms", kind, node, params, dur)
		return r.eng.InjectFault(ctx, run.Template, node, spec.FaultSpec{Kind: spec.FaultKind(kind), DurationMs: dur, Params: params})
	case "fault.clear":
		r.note(a, "clear faults on %s", node)
		return r.eng.ClearFaults(ctx, run.Template, node)
	case "control":
		op, _ := args["op"].(string)
		params, _ := args["params"].(map[string]any)
		r.note(a, "control %s %s %v", node, op, params)
		_, err := r.eng.Control(ctx, run.Template, node, control.Command{Op: op, Params: params})
		return err
	case "config.set":
		r.note(a, "config.set %s %v", node, args)
		_, err := r.eng.Control(ctx, run.Template, node, control.ConfigSet(args))
		return err
	case "resources.set":
		res := spec.Resources{}
		if f, ok := toFloat(args["cpus"]); ok {
			res.CPUs = f
		}
		if f, ok := toFloat(args["memory_mb"]); ok {
			res.MemoryMB = int(f)
		}
		if f, ok := toFloat(args["pids_max"]); ok {
			res.PidsMax = int(f)
		}
		if res == (spec.Resources{}) {
			if n, ok := run.Topology.NodeByID(node); ok {
				res = n.Resources // restore the template's limits
			}
		}
		r.note(a, "resources %s cpus=%g memory_mb=%d", node, res.CPUs, res.MemoryMB)
		return r.eng.SetResources(ctx, run.Template, node, res)
	case "restart":
		delay := 0.0
		if f, ok := toFloat(args["after_s"]); ok {
			delay = f
		}
		r.note(a, "kill %s, restart after %gs", node, delay)
		return r.eng.InjectFault(ctx, run.Template, node, spec.FaultSpec{Kind: spec.FaultKill, DurationMs: int(math.Max(delay, 0.5) * 1000), Params: map[string]any{}})
	case "replicas.set":
		n, _ := toFloat(args["replicas"])
		t := run.Topology.Clone()
		for i := range t.Nodes {
			if t.Nodes[i].ID == node {
				t.Nodes[i].Replicas = int(math.Max(1, n))
			}
		}
		r.note(a, "replicas %s -> %d (re-apply)", node, int(n))
		return r.eng.Apply(ctx, t)
	case "apply":
		r.note(a, "re-apply template")
		return r.eng.Apply(ctx, run.Topology)
	case "teardown":
		r.note(a, "teardown")
		return r.eng.Teardown(ctx, run.Template)
	}
	return fmt.Errorf("unknown step op %q", st.Do)
}
