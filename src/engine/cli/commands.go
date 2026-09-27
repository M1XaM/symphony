package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"symphony/blocks/common/control"
	"symphony/engine/core"
	"symphony/engine/events"
	"symphony/engine/spec"
	"symphony/engine/tests"
)

var commands = []command{
	// topologies
	{name: "templates", group: "Topologies", run: cmdTemplates,
		help: "list the built-in and the saved templates"},
	{name: "show", args: "<template|file>", group: "Topologies", run: cmdShow,
		help: "print a template as JSON\nEdit the output and give the file to apply, plan or save."},
	{name: "save", args: "<file|-> [--name NAME]", group: "Topologies", run: cmdSave,
		help: "save a topology JSON file (- reads stdin) as a template\nA template of the same name is replaced."},
	{name: "delete", args: "<template>", group: "Topologies", run: cmdDelete,
		help: "delete a saved template"},
	{name: "validate", args: "<template|file>", group: "Topologies", run: cmdValidate,
		help: "check a topology and list what is wrong with it"},
	{name: "plan", args: "<template|file> [--name NAME]", group: "Topologies", docker: wantDocker, run: cmdPlan,
		help: "list the operations apply would run, against what is running under that name\n--name applies the topology under another name."},
	{name: "apply", args: "<template|file> [--name NAME]", group: "Topologies", docker: needDocker, run: cmdApply,
		help: "apply a topology and wait until it runs\nA topology already running under that name is changed in place: hot config\nlive, cold config by recreating the node. --name applies it under another name."},
	{name: "teardown", args: "<deployment>", group: "Topologies", docker: needDocker, run: cmdTeardown,
		help: "remove a deployment's containers and network, and forget it"},
	{name: "restore", group: "Topologies", docker: needDocker, run: cmdRestore,
		help: "recreate stored deployments whose containers are gone or stopped\nThe desktop app does this by itself when it starts."},

	// running deployments
	{name: "status", group: "Deployments", docker: wantDocker, run: cmdStatus,
		help: "Docker, the state database and every deployment with its phase"},
	{name: "inspect", args: "<deployment> [node]", group: "Deployments", docker: needDocker, run: cmdInspect,
		help: "a deployment's nodes and edges, or one node's health checks, config and limits"},
	{name: "config", args: "<deployment> <node> key=value...", group: "Deployments", docker: needDocker, run: cmdConfig,
		help: "change hot config keys of a running node, live\nOnly keys the node's hot config already has can be set."},
	{name: "resources", args: "<deployment> <node> [--cpus N] [--memory-mb N] [--pids N]", group: "Deployments", docker: needDocker, run: cmdResources,
		help: "change a running node's CPU, memory and pid limits in place (0 = unlimited)"},
	{name: "fault", args: "<deployment> <node|edge> <kind> [--for DURATION] [key=value...]", group: "Deployments", docker: needDocker, run: cmdFault,
		help: "inject a fault into a node, or into the link an edge is\n" +
			"kinds: " + faultKinds() + "\n" +
			"--for ends it after e.g. 10s (default: until cleared). kill, pause and a whole-node\n" +
			"partition are done by Docker, so symphony-cli stays until they end; Ctrl-C ends them early.\n" +
			"key=value are the kind's knobs, e.g. ms=250 for latency, rate=0.2 for drop."},
	{name: "clear", args: "<deployment> <node|edge>", group: "Deployments", docker: needDocker, run: cmdClear,
		help: "clear every fault on a node, whoever injected it"},
	{name: "load", args: "<deployment> [stop | key=value...] [--node GATEWAY]", group: "Deployments", docker: needDocker, run: cmdLoad,
		help: "drive load through the gateway, stop it, or (no arguments) show it\n" +
			"keys: rps, pattern (constant|ramp|burst|sine|step), to_rps, factor, burst_ms,\n" +
			"period_ms, step_every_ms, step_rps, duration_ms (0 = until stopped), concurrency,\n" +
			"payload_bytes. A load without an end is restarted when the deployment is reopened."},
	{name: "control", args: "<deployment> <node> <op> [key=value...]", group: "Deployments", docker: needDocker, run: cmdControl,
		help: "send a raw /control command to every replica of a node and print the first answer\n" +
			"e.g. config.get, fault.list, sagas.summary, sagas.recent n=5, behaviour.get, topics.get"},
	{name: "logs", args: "<deployment> <node> [-n LINES]", group: "Deployments", docker: needDocker, run: cmdLogs,
		help: "a node's recent log lines"},
	{name: "top", args: "[deployment]", group: "Deployments", docker: needDocker, run: cmdTop,
		help: "live run metrics, one line a second, for one deployment or all of them (Ctrl-C ends)"},
	{name: "ps", args: "[-a]", group: "Deployments", docker: needDocker, run: cmdPs,
		help: "every container on the machine, like docker ps; -a includes stopped ones"},
	{name: "log", group: "Deployments", docker: needDocker, run: cmdLog,
		help: "follow what every container of Symphony prints, and the engine's own log (Ctrl-C ends)"},

	// tests
	{name: "scenarios", group: "Tests", run: cmdScenarios,
		help: "the predefined test scenarios"},
	{name: "test", args: "<scenario> <template|file> [--name NAME] [param=value...]", group: "Tests", docker: needDocker, run: cmdTest,
		help: "run a scenario against a template (applied first when it is not running) and wait\n" +
			"for the verdict; exits 3 when it does not pass. Ctrl-C aborts the run."},
	{name: "runs", group: "Tests", run: cmdRuns,
		help: "stored test runs, newest first"},
	{name: "run", args: "<id>", group: "Tests", run: cmdRun,
		help: "one run's phases, summary and assertions"},
	{name: "compare", args: "<id> <id>", group: "Tests", run: cmdCompare,
		help: "two runs' summaries side by side"},
	{name: "rm-run", args: "<id>", group: "Tests", run: cmdRmRun,
		help: "delete a stored run"},
}

func faultKinds() string {
	kinds := make([]string, len(spec.AllFaultKinds))
	for i, k := range spec.AllFaultKinds {
		kinds[i] = string(k)
	}
	return strings.Join(kinds, ", ")
}

// --- lookups -----------------------------------------------------------------

// topology resolves a template argument: a JSON file (- is stdin), a saved
// template, a built-in one, or the topology a deployment runs.
func (e *env) topology(arg string) (spec.Topology, error) {
	if arg == "-" || strings.HasSuffix(arg, ".json") || fileExists(arg) {
		var b []byte
		var err error
		if arg == "-" {
			b, err = io.ReadAll(os.Stdin)
		} else {
			b, err = os.ReadFile(arg)
		}
		if err != nil {
			return spec.Topology{}, err
		}
		var t spec.Topology
		if err := json.Unmarshal(b, &t); err != nil {
			return spec.Topology{}, fmt.Errorf("%s: %w", arg, err)
		}
		return t.Normalized(), nil
	}
	saved, err := e.a.GetUserTemplates()
	if err != nil {
		saved = nil // no config directory: presets and deployments still work
	}
	candidates := append(saved, e.a.GetPresets()...)
	for _, d := range e.a.GetDeployments() {
		candidates = append(candidates, d.Topology)
	}
	for _, t := range candidates {
		if t.Name == arg {
			return t, nil
		}
	}
	for _, t := range candidates {
		if strings.EqualFold(t.Name, arg) {
			return t, nil
		}
	}
	return spec.Topology{}, fmt.Errorf("no template, file or deployment called %q (see symphony-cli templates)", arg)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// deployment finds a running deployment by name (case-insensitively if
// there is no exact match).
func (e *env) deployment(name string) (core.DeploymentInfo, error) {
	deps := e.a.GetDeployments()
	for _, d := range deps {
		if d.Name == name {
			return d, nil
		}
	}
	for _, d := range deps {
		if strings.EqualFold(d.Name, name) {
			return d, nil
		}
	}
	names := make([]string, len(deps))
	for i, d := range deps {
		names[i] = fmt.Sprintf("%q", d.Name)
	}
	if len(names) == 0 {
		return core.DeploymentInfo{}, fmt.Errorf("%q is not applied, and nothing else is", name)
	}
	return core.DeploymentInfo{}, fmt.Errorf("%q is not applied (running: %s)", name, strings.Join(names, ", "))
}

// target resolves a node id, or an edge id to the node that sends on it:
// faults on an edge are injected at its sender, scoped to that link.
func target(d core.DeploymentInfo, arg string) (node, edge string, err error) {
	if _, ok := d.Topology.NodeByID(arg); ok {
		return arg, "", nil
	}
	for _, ed := range d.Topology.Edges {
		if ed.ID == arg {
			return ed.From, ed.ID, nil
		}
	}
	ids := make([]string, 0, len(d.Topology.Nodes))
	for _, n := range d.Topology.Nodes {
		ids = append(ids, n.ID)
	}
	return "", "", fmt.Errorf("%q has no node or edge %q (nodes: %s)", d.Name, arg, strings.Join(ids, ", "))
}

func ready(d core.DeploymentInfo) (up, total int) {
	for _, n := range d.Topology.Nodes {
		total++
		if st, ok := d.Statuses[n.ID]; ok && (st.State == "healthy" || st.State == "degraded") {
			up++
		}
	}
	return up, total
}

// --- topologies --------------------------------------------------------------

func cmdTemplates(e *env, args []string) error {
	if len(args) > 0 {
		return usagef("takes no arguments")
	}
	type row struct {
		Name    string `json:"name"`
		Source  string `json:"source"`
		Nodes   int    `json:"nodes"`
		Edges   int    `json:"edges"`
		Applied bool   `json:"applied"`
	}
	applied := map[string]bool{}
	if recs, err := e.b.Store.Deployments(); err == nil {
		for _, r := range recs {
			applied[r.Name] = true
		}
	}
	var out []row
	for _, t := range e.a.GetPresets() {
		out = append(out, row{t.Name, "built-in", len(t.Nodes), len(t.Edges), applied[t.Name]})
	}
	saved, err := e.a.GetUserTemplates()
	if err != nil {
		return err
	}
	for _, t := range saved {
		out = append(out, row{t.Name, "saved", len(t.Nodes), len(t.Edges), applied[t.Name]})
	}
	if e.json {
		return e.printJSON(out)
	}
	rows := make([][]string, len(out))
	for i, r := range out {
		mark := ""
		if r.Applied {
			mark = "applied"
		}
		rows[i] = []string{r.Name, r.Source, fmt.Sprint(r.Nodes), fmt.Sprint(r.Edges), mark}
	}
	e.table("TEMPLATE\tSOURCE\tNODES\tEDGES\t", rows)
	return nil
}

func cmdShow(e *env, args []string) error {
	if len(args) != 1 {
		return usagef("needs one template or file")
	}
	t, err := e.topology(args[0])
	if err != nil {
		return err
	}
	return e.printJSON(t)
}

func cmdSave(e *env, args []string) error {
	fset := flag.NewFlagSet("save", flag.ContinueOnError)
	name := fset.String("name", "", "")
	pos, err := parse(fset, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("needs one file")
	}
	if pos[0] != "-" && !fileExists(pos[0]) {
		return fmt.Errorf("%s: no such file", pos[0])
	}
	t, err := e.topology(pos[0])
	if err != nil {
		return err
	}
	if *name != "" {
		t.Name = *name
	}
	if _, err := e.a.SaveUserTemplate(t); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "saved template %q\n", t.Name)
	return nil
}

func cmdDelete(e *env, args []string) error {
	if len(args) != 1 {
		return usagef("needs one template name")
	}
	before, err := e.a.GetUserTemplates()
	if err != nil {
		return err
	}
	after, err := e.a.DeleteUserTemplate(args[0])
	if err != nil {
		return err
	}
	if len(after) == len(before) {
		return fmt.Errorf("no saved template %q (built-in ones cannot be deleted)", args[0])
	}
	fmt.Fprintf(e.out, "deleted template %q\n", args[0])
	return nil
}

func cmdValidate(e *env, args []string) error {
	if len(args) != 1 {
		return usagef("needs one template or file")
	}
	t, err := e.topology(args[0])
	if err != nil {
		return err
	}
	findings := e.a.ValidateTopology(t)
	if e.json {
		if err := e.printJSON(findings); err != nil {
			return err
		}
	} else if len(findings) == 0 {
		fmt.Fprintf(e.out, "%q is valid\n", t.Name)
	} else {
		for _, f := range findings {
			fmt.Fprintf(e.out, "%-7s %s\n", f.Severity, f.Error())
		}
	}
	if spec.HasErrors(findings) {
		return errors.New("the topology has errors")
	}
	return nil
}

func cmdPlan(e *env, args []string) error {
	fset := flag.NewFlagSet("plan", flag.ContinueOnError)
	name := fset.String("name", "", "")
	pos, err := parse(fset, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("needs one template or file")
	}
	t, err := e.topology(pos[0])
	if err != nil {
		return err
	}
	if *name != "" {
		t.Name = *name
	}
	plan, err := e.a.PlanApply(t)
	if err != nil {
		return err
	}
	if e.json {
		return e.printJSON(plan)
	}
	if !e.docker.Available {
		fmt.Fprintln(e.out, "(Docker is not available: planned as if nothing were running)")
	}
	rows := make([][]string, len(plan.Ops))
	for i, op := range plan.Ops {
		rows[i] = []string{fmt.Sprint(i + 1), string(op.Kind), op.Target, string(op.Class), op.Reason}
	}
	e.table("#\tOPERATION\tTARGET\tCLASS\tREASON", rows)
	s := plan.Summary
	fmt.Fprintf(e.out, "\n%d operations: %d hot, %d cold, %d structural (%d creates, %d destroys)\n",
		len(plan.Ops), s.Hot, s.Cold, s.Structural, s.Creates, s.Destroys)
	return nil
}

func cmdApply(e *env, args []string) error {
	fset := flag.NewFlagSet("apply", flag.ContinueOnError)
	name := fset.String("name", "", "")
	pos, err := parse(fset, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("needs one template or file")
	}
	t, err := e.topology(pos[0])
	if err != nil {
		return err
	}
	if *name != "" {
		t.Name = *name
	}
	fmt.Fprintf(e.out, "applying %q\n", t.Name)
	defer e.follow(t.Name)()
	start := time.Now()
	if err := e.a.Apply(t); err != nil {
		return err
	}
	d, err := e.deployment(t.Name)
	if err != nil {
		fmt.Fprintf(e.out, "%q applied: nothing left to run\n", t.Name)
		return nil
	}
	up, total := ready(d)
	fmt.Fprintf(e.out, "%q is %s: %d/%d nodes up (%s)\n", d.Name, d.Phase, up, total, time.Since(start).Round(100*time.Millisecond))
	return nil
}

func cmdTeardown(e *env, args []string) error {
	if len(args) != 1 {
		return usagef("needs one deployment")
	}
	d, err := e.deployment(args[0])
	if err != nil {
		return err
	}
	fmt.Fprintf(e.out, "tearing down %q\n", d.Name)
	defer e.follow(d.Name)()
	if err := e.a.Teardown(d.Name); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "%q is gone\n", d.Name)
	return nil
}

func cmdRestore(e *env, args []string) error {
	if len(args) > 0 {
		return usagef("takes no arguments")
	}
	broken, err := e.b.Engine.Attach(e.ctx)
	if err != nil {
		return err
	}
	e.b.Engine.CollectOrphans(e.ctx)
	if len(broken) == 0 {
		fmt.Fprintln(e.out, "every stored deployment is running as stored")
		return nil
	}
	failed := 0
	for _, name := range broken {
		fmt.Fprintf(e.out, "reopening %q\n", name)
		stop := e.follow(name)
		err := e.b.Engine.Reopen(e.ctx, name)
		stop()
		if err != nil {
			fmt.Fprintf(e.out, "%q: %v\n", name, err)
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d deployment(s) could not be reopened", failed, len(broken))
	}
	return nil
}

// --- deployments -------------------------------------------------------------

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
	_, peers := params["peers"]
	byDocker := kind == spec.FaultKill || kind == spec.FaultPause || (kind == spec.FaultPartition && !peers)
	switch {
	case *dur == 0:
		fmt.Fprintf(e.out, "%s: %s on %s until cleared (symphony-cli clear %q %s)\n", d.Name, kind, where, d.Name, node)
	case !byDocker:
		fmt.Fprintf(e.out, "%s: %s on %s for %s\n", d.Name, kind, where, *dur)
	default:
		// Docker does not end these by itself: this process has to, so it
		// stays until then.
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
	cmd := control.Command{Op: "load.status"}
	switch {
	case len(pos) == 2 && pos[1] == "stop":
		cmd.Op = "load.stop"
	case len(pos) > 1:
		params, err := keyValues(pos[1:])
		if err != nil {
			return err
		}
		cmd = control.Command{Op: "load.set", Params: params}
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
	for {
		select {
		case <-e.ctx.Done():
			return nil
		case m := <-ticks:
			if time.Since(last) < 900*time.Millisecond {
				continue
			}
			last = time.Now()
			if e.json {
				_ = json.NewEncoder(e.out).Encode(m)
				continue
			}
			if lines%20 == 0 {
				fmt.Fprintln(e.out, header)
			}
			lines++
			fmt.Fprintf(e.out, "%s %8.1f %8.1f %8.1f %6.1f%% %7d %7.1f %7d\n", time.UnixMilli(m.Ts).Format("15:04:05"),
				m.SagasPerSec, m.LatencyMs, m.P99Ms, m.ErrorRate*100, m.ConsumerLag, m.CPUPct, m.ActiveFaults)
		}
	}
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
	if len(args) > 0 {
		return usagef("takes no arguments")
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
		case l := <-lines:
			if e.json {
				_ = json.NewEncoder(e.out).Encode(l)
			} else {
				fmt.Fprintln(e.out, logLine(l))
			}
		}
	}
}

// --- tests -------------------------------------------------------------------

func cmdScenarios(e *env, args []string) error {
	if len(args) > 0 {
		return usagef("takes no arguments")
	}
	list := e.a.ListScenarios()
	if e.json {
		return e.printJSON(list)
	}
	rows := make([][]string, len(list))
	for i, s := range list {
		var params []string
		for _, k := range sortedKeys(s.Params) {
			params = append(params, k+"="+fmtValue(s.Params[k]))
		}
		rows[i] = []string{s.ID, s.Name, strings.Join(params, " ")}
	}
	e.table("ID\tNAME\tPARAMETERS", rows)
	return nil
}

func cmdTest(e *env, args []string) error {
	fset := flag.NewFlagSet("test", flag.ContinueOnError)
	name := fset.String("name", "", "")
	pos, err := parse(fset, args)
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return usagef("needs a scenario and a template")
	}
	t, err := e.topology(pos[1])
	if err != nil {
		return err
	}
	if *name != "" {
		t.Name = *name
	}
	params, err := keyValues(pos[2:])
	if err != nil {
		return err
	}
	scenario := pos[0]

	done := make(chan events.TestDonePayload, 1)
	var lastProgress time.Time
	defer e.b.Bus.Subscribe(func(ev events.Event) {
		switch p := ev.Payload.(type) {
		case events.TestProgressPayload:
			if p.Scenario != scenario || p.Template != t.Name || time.Since(lastProgress) < 2*time.Second {
				return
			}
			lastProgress = time.Now()
			fmt.Fprintf(e.out, "  %4.0fs/%.0fs  %-10s %6.1f sagas/s  p99 %6.1f ms  %5.1f%% failed\n", float64(p.ElapsedMs)/1000, float64(p.TotalMs)/1000,
				p.Phase, p.Live["sagas_per_sec"], p.Live["latency_p99_ms"], p.Live["error_rate"]*100)
		case events.TestDonePayload:
			if p.Scenario == scenario && p.Template == t.Name {
				done <- p
			}
		}
	})()
	defer e.follow(t.Name)()
	// the run collects the series the poller and the collector publish
	e.b.Engine.Start(e.ctx)
	e.b.Metrics.Start(e.ctx)

	id, err := e.a.StartTest(scenario, t, params)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.out, "running %s on %q (run %s)\n", scenario, t.Name, id)
	var result events.TestDonePayload
	select {
	case result = <-done:
	case <-e.ctx.Done():
		fmt.Fprintln(e.out, "aborting…")
		_ = e.a.AbortTest(id)
		select {
		case result = <-done:
		case <-time.After(30 * time.Second):
			return e.ctx.Err()
		}
	}
	run, err := e.a.GetRun(result.RunID)
	if err != nil {
		return err
	}
	if e.json {
		return e.printJSON(run)
	}
	fmt.Fprintln(e.out)
	printRun(e, run)
	if !run.Passed {
		return errFailed
	}
	return nil
}

func verdict(passed, aborted bool, running bool) string {
	switch {
	case running:
		return "running"
	case aborted:
		return "aborted"
	case passed:
		return "passed"
	}
	return "failed"
}

func cmdRuns(e *env, args []string) error {
	if len(args) > 0 {
		return usagef("takes no arguments")
	}
	runs, err := e.a.ListRuns()
	if err != nil {
		return err
	}
	if e.json {
		return e.printJSON(runs)
	}
	rows := make([][]string, len(runs))
	for i, r := range runs {
		took := "-"
		if r.EndedAt > 0 {
			took = (time.Duration(r.EndedAt-r.StartedAt) * time.Millisecond).Round(time.Second).String()
		}
		rows[i] = []string{r.ID, r.Scenario, r.Template, time.UnixMilli(r.StartedAt).Format("2006-01-02 15:04"), took, verdict(r.Passed, r.Aborted, r.Running)}
	}
	e.table("RUN\tSCENARIO\tTEMPLATE\tSTARTED\tTOOK\tVERDICT", rows)
	return nil
}

func cmdRun(e *env, args []string) error {
	if len(args) != 1 {
		return usagef("needs a run id")
	}
	run, err := e.a.GetRun(args[0])
	if err != nil {
		return err
	}
	if e.json {
		return e.printJSON(run)
	}
	printRun(e, run)
	return nil
}

func printRun(e *env, run tests.Run) {
	fmt.Fprintf(e.out, "%s: %s on %q, %s\n", run.ID, run.Name, run.Template, strings.ToUpper(verdict(run.Passed, run.Aborted, false)))
	if run.Error != "" {
		fmt.Fprintf(e.out, "error: %s\n", run.Error)
	}
	if len(run.Phases) > 0 {
		var phases []string
		for _, p := range run.Phases {
			phases = append(phases, fmt.Sprintf("%s %.0f-%.0fs", p.Name, p.Start, p.End))
		}
		fmt.Fprintf(e.out, "phases: %s\n", strings.Join(phases, ", "))
	}
	if len(run.Assertions) > 0 {
		fmt.Fprintln(e.out)
		rows := make([][]string, len(run.Assertions))
		for i, a := range run.Assertions {
			ok := "FAIL"
			if a.Passed {
				ok = "ok"
			}
			rows[i] = []string{ok, a.Metric, a.Op + " " + fmt.Sprintf("%g", a.Value), orDash(a.Phase), fmt.Sprintf("%g", a.Actual)}
		}
		e.table("\tASSERTION\tEXPECTED\tPHASE\tACTUAL", rows)
	}
	if len(run.Summary) > 0 {
		fmt.Fprintln(e.out)
		rows := make([][]string, 0, len(run.Summary))
		for _, k := range sortedKeys(run.Summary) {
			if !strings.Contains(k, "@") {
				rows = append(rows, []string{k, fmt.Sprintf("%g", run.Summary[k])})
			}
		}
		e.table("SUMMARY\tVALUE", rows)
	}
}

func cmdCompare(e *env, args []string) error {
	if len(args) != 2 {
		return usagef("needs two run ids")
	}
	a, err := e.a.GetRun(args[0])
	if err != nil {
		return err
	}
	b, err := e.a.GetRun(args[1])
	if err != nil {
		return err
	}
	if e.json {
		return e.printJSON([]tests.Run{a, b})
	}
	keys := map[string]bool{}
	for k := range a.Summary {
		keys[k] = true
	}
	for k := range b.Summary {
		keys[k] = true
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	cell := func(r tests.Run, k string) string {
		if v, ok := r.Summary[k]; ok {
			return fmt.Sprintf("%g", v)
		}
		return "-"
	}
	rows := [][]string{
		{"template", a.Template, b.Template},
		{"verdict", verdict(a.Passed, a.Aborted, false), verdict(b.Passed, b.Aborted, false)},
	}
	for _, k := range names {
		rows = append(rows, []string{k, cell(a, k), cell(b, k)})
	}
	e.table(fmt.Sprintf("\t%s\t%s", a.ID, b.ID), rows)
	return nil
}

func cmdRmRun(e *env, args []string) error {
	if len(args) != 1 {
		return usagef("needs a run id")
	}
	if err := e.a.DeleteRun(args[0]); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "deleted run %s\n", args[0])
	return nil
}
