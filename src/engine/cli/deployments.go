// The commands on running deployments: status, inspect, config, limits,
// faults, load, control, logs.

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"symphony/blocks/common/control"
	"symphony/engine/core"
	"symphony/engine/events"
	"symphony/engine/spec"
)

func cmdStatus(e *env, args []string) error {
	if len(args) > 0 {
		return usagef("takes no arguments")
	}
	deps := e.a.GetDeployments()
	if !e.docker.Available {
		// the containers cannot be asked: show what the database says
		recs, err := e.b.Store.Deployments()
		if err != nil {
			return err
		}
		deps = deps[:0]
		for _, r := range recs {
			deps = append(deps, core.DeploymentInfo{Name: r.Name, Phase: core.Phase(r.Phase), Network: r.Network, Topology: r.Topology})
		}
	}
	if e.json {
		return e.printJSON(map[string]any{"docker": e.docker, "database": e.b.Store.Path(), "deployments": deps})
	}
	dockerLine := "not available: " + e.docker.Detail
	if e.docker.Available {
		dockerLine = "connected, " + e.docker.Detail
	}
	fmt.Fprintf(e.out, "docker    %s\ndatabase  %s\n\n", dockerLine, orDash(e.b.Store.Path()))
	if len(deps) == 0 {
		fmt.Fprintln(e.out, "nothing is applied")
		return nil
	}
	rows := make([][]string, len(deps))
	for i, d := range deps {
		up, total := ready(d)
		nodes := fmt.Sprintf("%d/%d", up, total)
		if !e.docker.Available {
			nodes = fmt.Sprintf("?/%d", total)
		}
		rows[i] = []string{d.Name, string(d.Phase), nodes, d.Network}
	}
	e.table("DEPLOYMENT\tPHASE\tNODES UP\tNETWORK", rows)
	return nil
}

func cmdInspect(e *env, args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return usagef("needs a deployment and optionally a node")
	}
	d, err := e.deployment(args[0])
	if err != nil {
		return err
	}
	if len(args) == 1 {
		if e.json {
			return e.printJSON(d)
		}
		up, total := ready(d)
		fmt.Fprintf(e.out, "%s: %s, %d/%d nodes up, network %s\n\n", d.Name, d.Phase, up, total, d.Network)
		rows := make([][]string, 0, len(d.Topology.Nodes))
		for _, n := range d.Topology.Nodes {
			st := d.Statuses[n.ID]
			rows = append(rows, []string{n.ID, string(n.Kind), n.Image, fmt.Sprintf("%d/%d", st.Ready, n.Replicas), orDash(st.State),
				fmtLimits(n.Resources), orDash(st.Message)})
		}
		e.table("NODE\tKIND\tIMAGE\tREADY\tSTATE\tLIMITS\tMESSAGE", rows)
		fmt.Fprintln(e.out)
		rows = rows[:0]
		for _, ed := range d.Topology.Edges {
			rows = append(rows, []string{ed.ID, ed.From + " -> " + ed.To, string(ed.Kind), orDash(ed.Topic), orDash(ed.Via)})
		}
		e.table("EDGE\tLINK\tKIND\tTOPIC\tVIA", rows)
		return nil
	}
	n, ok := d.Topology.NodeByID(args[1])
	if !ok {
		_, _, err := target(d, args[1])
		return err
	}
	st := d.Statuses[n.ID]
	if e.json {
		return e.printJSON(map[string]any{"node": n, "status": st})
	}
	fmt.Fprintf(e.out, "%s (%s, %s)\nstate     %s, %d/%d replicas ready", n.ID, n.Kind, n.Image, orDash(st.State), st.Ready, n.Replicas)
	if st.Message != "" {
		fmt.Fprintf(e.out, ", %s", st.Message)
	}
	fmt.Fprintf(e.out, "\nlimits    %s\n\n", fmtLimits(n.Resources))
	if len(st.Checks) > 0 {
		rows := make([][]string, len(st.Checks))
		for i, c := range st.Checks {
			var detail []string
			for _, k := range sortedKeys(c.Detail) {
				detail = append(detail, k+"="+fmtValue(c.Detail[k]))
			}
			rows[i] = []string{c.Name, c.Status, strings.Join(detail, " ")}
		}
		e.table("CHECK\tSTATUS\tDETAIL", rows)
		fmt.Fprintln(e.out)
	}
	printConfig := func(title string, cfg map[string]any) {
		if len(cfg) == 0 {
			return
		}
		fmt.Fprintln(e.out, title)
		for _, k := range sortedKeys(cfg) {
			fmt.Fprintf(e.out, "  %s = %s\n", k, fmtValue(cfg[k]))
		}
	}
	printConfig("hot config (config sets these live)", n.Config.Hot)
	printConfig("cold config (changed by re-applying the topology)", n.Config.Cold)
	return nil
}

func fmtLimits(r spec.Resources) string {
	cpu, mem := "no cpu limit", "no memory limit"
	if r.CPUs > 0 {
		cpu = fmt.Sprintf("%g cpu", r.CPUs)
	}
	if r.MemoryMB > 0 {
		mem = fmt.Sprintf("%d MB", r.MemoryMB)
	}
	return cpu + ", " + mem
}

func cmdConfig(e *env, args []string) error {
	if len(args) < 3 {
		return usagef("needs a deployment, a node and at least one key=value")
	}
	d, err := e.deployment(args[0])
	if err != nil {
		return err
	}
	patch, err := keyValues(args[2:])
	if err != nil {
		return err
	}
	if err := e.a.SetHotConfig(d.Name, args[1], patch); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "%s/%s: set %s (live, no restart)\n", d.Name, args[1], strings.Join(args[2:], ", "))
	return nil
}

func cmdResources(e *env, args []string) error {
	fset := flag.NewFlagSet("resources", flag.ContinueOnError)
	cpus := fset.Float64("cpus", 0, "")
	mem := fset.Int("memory-mb", 0, "")
	pids := fset.Int("pids", 0, "")
	pos, err := parse(fset, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usagef("needs a deployment and a node")
	}
	d, err := e.deployment(pos[0])
	if err != nil {
		return err
	}
	n, ok := d.Topology.NodeByID(pos[1])
	if !ok {
		_, _, err := target(d, pos[1])
		return err
	}
	r := n.Resources
	set := 0
	fset.Visit(func(f *flag.Flag) {
		set++
		switch f.Name {
		case "cpus":
			r.CPUs = *cpus
		case "memory-mb":
			r.MemoryMB = *mem
		case "pids":
			r.PidsMax = *pids
		}
	})
	if set == 0 {
		fmt.Fprintf(e.out, "%s/%s: %s\n", d.Name, n.ID, fmtLimits(r))
		return nil
	}
	if err := e.a.SetResources(d.Name, n.ID, r); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "%s/%s: %s (in place)\n", d.Name, n.ID, fmtLimits(r))
	return nil
}

func cmdFault(e *env, args []string) error {
	fset := flag.NewFlagSet("fault", flag.ContinueOnError)
	dur := fset.Duration("for", 0, "")
	pos, err := parse(fset, args)
	if err != nil {
		return err
	}
	if len(pos) < 3 {
		return usagef("needs a deployment, a node or edge, and a fault kind")
	}
	kind := spec.FaultKind(pos[2])
	if !kind.Valid() {
		return usagef("unknown fault kind %q (kinds: %s)", pos[2], faultKinds())
	}
	d, err := e.deployment(pos[0])
	if err != nil {
		return err
	}
	node, edge, err := target(d, pos[1])
	if err != nil {
		return err
	}
	params, err := keyValues(pos[3:])
	if err != nil {
		return err
	}
	if edge != "" {
		params["edge"] = edge
		params["direction"] = "out"
	}
	fault := spec.FaultSpec{Kind: kind, DurationMs: int(dur.Milliseconds()), Params: params}
	if err := e.a.InjectFault(d.Name, node, fault); err != nil {
		return err
	}
	where := node
	if edge != "" {
		where = fmt.Sprintf("%s (link %s)", node, edge)
	}
	switch {
	case *dur == 0:
		fmt.Fprintf(e.out, "%s: %s on %s until cleared (symphony-cli clear %q %s)\n", d.Name, kind, where, d.Name, node)
	case !fault.NeedsRevert():
		fmt.Fprintf(e.out, "%s: %s on %s for %s\n", d.Name, kind, where, *dur)
	default:
		// Nothing ends these by itself: this process does, so it stays
		// until then. (Should it die first, the fault is stored and the
		// next Symphony to open ends it.)
		fmt.Fprintf(e.out, "%s: %s on %s for %s (Ctrl-C ends it now)\n", d.Name, kind, where, *dur)
		select {
		case <-time.After(*dur):
		case <-e.ctx.Done():
		}
		// not e.ctx: after Ctrl-C it is cancelled, and this must still run
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := e.b.Engine.ClearFaults(ctx, d.Name, node); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "%s: %s on %s ended\n", d.Name, kind, where)
	}
	return nil
}

func cmdClear(e *env, args []string) error {
	if len(args) != 2 {
		return usagef("needs a deployment and a node or edge")
	}
	d, err := e.deployment(args[0])
	if err != nil {
		return err
	}
	node, _, err := target(d, args[1])
	if err != nil {
		return err
	}
	if err := e.a.ClearFaults(d.Name, node); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "%s: cleared every fault on %s\n", d.Name, node)
	return nil
}

func cmdLoad(e *env, args []string) error {
	fset := flag.NewFlagSet("load", flag.ContinueOnError)
	gw := fset.String("node", "", "")
	pos, err := parse(fset, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 {
		return usagef("needs a deployment")
	}
	d, err := e.deployment(pos[0])
	if err != nil {
		return err
	}
	if *gw == "" {
		for _, n := range d.Topology.Nodes {
			if n.Kind == spec.KindInfra {
				*gw = n.ID
				break
			}
		}
		if *gw == "" {
			return fmt.Errorf("%q has no gateway to drive load through", d.Name)
		}
	}
	cmd := control.LoadStatus()
	switch {
	case len(pos) == 2 && pos[1] == "stop":
		cmd = control.LoadStop()
	case len(pos) > 1:
		params, err := keyValues(pos[1:])
		if err != nil {
			return err
		}
		cmd = control.LoadSet(params)
	}
	res, err := e.a.ControlNode(d.Name, *gw, cmd)
	if err != nil {
		return err
	}
	if !res.OK {
		return errors.New(res.Error)
	}
	if e.json {
		return e.printJSON(res.State)
	}
	st, _ := res.State.(map[string]any)
	if running, _ := st["running"].(bool); !running {
		fmt.Fprintf(e.out, "%s/%s: no load (%v sagas sent so far)\n", d.Name, *gw, fmtValue(st["sent"]))
		return nil
	}
	fmt.Fprintf(e.out, "%s/%s: driving load\n", d.Name, *gw)
	for _, k := range sortedKeys(st) {
		if k != "running" {
			fmt.Fprintf(e.out, "  %s = %s\n", k, fmtValue(st[k]))
		}
	}
	return nil
}

func cmdControl(e *env, args []string) error {
	if len(args) < 3 {
		return usagef("needs a deployment, a node and an op")
	}
	d, err := e.deployment(args[0])
	if err != nil {
		return err
	}
	params, err := keyValues(args[3:])
	if err != nil {
		return err
	}
	res, err := e.a.ControlNode(d.Name, args[1], control.Command{Op: args[2], Params: params})
	if err != nil {
		return err
	}
	if !res.OK {
		return errors.New(res.Error)
	}
	return e.printJSON(res.State)
}

func cmdLogs(e *env, args []string) error {
	fset := flag.NewFlagSet("logs", flag.ContinueOnError)
	n := fset.Int("n", 100, "")
	container := fset.String("container", "", "")
	pos, err := parse(fset, args)
	if err != nil {
		return err
	}
	if *container != "" {
		if len(pos) > 0 {
			return usagef("--container takes the place of a deployment and a node")
		}
		lines, err := e.a.GetContainerLogs(*container, *n)
		if err != nil {
			return err
		}
		if e.json {
			return e.printJSON(lines)
		}
		for _, l := range lines {
			ts := "            "
			if l.Ts > 0 {
				ts = time.UnixMilli(l.Ts).Format("15:04:05.000")
			}
			fmt.Fprintf(e.out, "%s %-5s %s\n", ts, strings.ToUpper(l.Level), l.Message)
		}
		return nil
	}
	if len(pos) != 2 {
		return usagef("needs a deployment and a node, or --container")
	}
	d, err := e.deployment(pos[0])
	if err != nil {
		return err
	}
	lines, err := e.a.GetNodeLogs(d.Name, pos[1], *n)
	if err != nil {
		return err
	}
	if e.json {
		return e.printJSON(lines)
	}
	for _, l := range lines {
		var attrs []string
		for _, k := range sortedKeys(l.Attrs) {
			if k != "container" {
				attrs = append(attrs, k+"="+fmtValue(l.Attrs[k]))
			}
		}
		fmt.Fprintf(e.out, "%s %-5s %s %s %s\n", time.UnixMilli(l.Ts).Format("15:04:05.000"), l.Level, fmtValue(l.Attrs["container"]), l.Msg, strings.Join(attrs, " "))
	}
	return nil
}

func cmdTop(e *env, args []string) error {
	fset := flag.NewFlagSet("top", flag.ContinueOnError)
	count := fset.Int("count", 0, "")
	args, err := parse(fset, args)
	if err != nil {
		return err
	}
	if len(args) > 1 {
		return usagef("takes at most one deployment")
	}
	name := ""
	if len(args) == 1 {
		d, err := e.deployment(args[0])
		if err != nil {
			return err
		}
		name = d.Name
	}
	ticks := make(chan events.MetricsPayload, 16)
	defer e.b.Bus.Subscribe(func(ev events.Event) {
		if m, ok := ev.Payload.(events.MetricsPayload); ok && m.Deployment == name {
			select {
			case ticks <- m:
			default:
			}
		}
	})()
	e.b.Engine.Start(e.ctx)
	e.b.Metrics.Start(e.ctx)
	header := "TIME      SAGAS/S   P50 MS   P99 MS  FAILED     LAG   CPU %  FAULTS"
	last, lines := time.Time{}, 0
	// the collector's first sample is only its baseline: all zeros
	baseline := true
	for *count <= 0 || lines < *count {
		select {
		case <-e.ctx.Done():
			return nil
		case m := <-ticks:
			if baseline {
				baseline = false
				continue
			}
			if time.Since(last) < 900*time.Millisecond {
				continue
			}
			last = time.Now()
			lines++
			if e.json {
				_ = json.NewEncoder(e.out).Encode(m)
				continue
			}
			if lines%20 == 1 {
				fmt.Fprintln(e.out, header)
			}
			fmt.Fprintf(e.out, "%s %8.1f %8.1f %8.1f %6.1f%% %7d %7.1f %7d\n", time.UnixMilli(m.Ts).Format("15:04:05"),
				m.SagasPerSec, m.LatencyMs, m.P99Ms, m.ErrorRate*100, m.ConsumerLag, m.CPUPct, m.ActiveFaults)
		}
	}
	return nil
}

func cmdPs(e *env, args []string) error {
	fset := flag.NewFlagSet("ps", flag.ContinueOnError)
	all := fset.Bool("a", false, "")
	pos, err := parse(fset, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("takes no arguments")
	}
	cs, err := e.a.ListContainers(*all)
	if err != nil {
		return err
	}
	if e.json {
		return e.printJSON(cs)
	}
	rows := make([][]string, len(cs))
	for i, c := range cs {
		ours := ""
		if c.Ours {
			ours = c.Deployment + "/" + c.Node
		}
		rows[i] = []string{c.ID, c.Image, c.Status, strings.Join(c.Ports, ", "), c.Name, ours}
	}
	e.table("CONTAINER ID\tIMAGE\tSTATUS\tPORTS\tNAMES\tSYMPHONY", rows)
	return nil
}

func cmdLog(e *env, args []string) error {
	fset := flag.NewFlagSet("log", flag.ContinueOnError)
	dur := fset.Duration("for", 0, "")
	args, err := parse(fset, args)
	if err != nil {
		return err
	}
	if len(args) > 0 {
		return usagef("takes no arguments")
	}
	var end <-chan time.Time
	if *dur > 0 {
		end = time.After(*dur)
	}
	e.echo.Store(false)
	lines := make(chan events.LogPayload, 256)
	defer e.b.Bus.Subscribe(func(ev events.Event) {
		if l, ok := ev.Payload.(events.LogPayload); ok && (e.verbose || l.Level != "debug") {
			select {
			case lines <- l:
			default: // a flood: the terminal cannot keep up anyway
			}
		}
	})()
	e.b.Logs.Start(e.ctx)
	for {
		select {
		case <-e.ctx.Done():
			return nil
		case <-end:
			return nil
		case l := <-lines:
			if e.json {
				_ = json.NewEncoder(e.out).Encode(l)
			} else {
				fmt.Fprintln(e.out, logLine(l))
			}
		}
	}
}
