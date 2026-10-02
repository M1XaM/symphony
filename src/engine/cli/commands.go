package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"symphony/engine/core"
	"symphony/engine/spec"
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
			"--for ends it after e.g. 10s (default: until cleared). kill, pause, crash and a whole-node\n" +
			"partition do not end by themselves, so symphony-cli stays until they do; Ctrl-C ends them early.\n" +
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
