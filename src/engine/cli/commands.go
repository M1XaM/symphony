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

// The command table: what `symphony-cli help`, `<command> --help`,
// `--json-help` and the generated GUIDE.md are all written from. A command
// described here needs no other documentation.
var commands []command

// built in init: the commands refer to the table through find
func init() {
	commands = []command{
		// topologies
		{name: "templates", group: "Topologies", run: cmdTemplates,
			help:     "list the built-in and the saved templates",
			examples: []string{`symphony-cli templates`}},
		{name: "show", args: "<template|file>", group: "Topologies", run: cmdShow,
			help:     "print a template as JSON\nEdit the output and give the file to apply, plan or save.",
			examples: []string{`symphony-cli show "Orchestration (saga)" > my-system.json`}},
		{name: "save", args: "<file|-> [--name NAME]", group: "Topologies", run: cmdSave, changes: true,
			help:     "save a topology JSON file (- reads stdin) as a template\nA template of the same name is replaced.",
			flags:    []flagDoc{{"--name", "NAME", "save it under this name instead of the one in the file"}},
			examples: []string{`symphony-cli save my-system.json --name "My system"`}},
		{name: "delete", args: "<template>", group: "Topologies", run: cmdDelete, changes: true,
			help:     "delete a saved template",
			examples: []string{`symphony-cli delete "My system"`}},
		{name: "validate", args: "<template|file>", group: "Topologies", run: cmdValidate,
			help:     "check a topology and list what is wrong with it\nExits 1 when it has errors; warnings do not fail it.",
			examples: []string{`symphony-cli validate my-system.json`}},
		{name: "plan", args: "<template|file> [--name NAME]", group: "Topologies", docker: wantDocker, run: cmdPlan,
			help:     "list the operations apply would run, against what is running under that name\nNothing is changed.",
			flags:    []flagDoc{{"--name", "NAME", "plan it under this deployment name"}},
			examples: []string{`symphony-cli plan my-system.json`}},
		{name: "apply", args: "<template|file> [--name NAME]", group: "Topologies", docker: needDocker, run: cmdApply, changes: true,
			blocks: "until every node is healthy (the first apply builds the block images, which takes a minute or two)",
			help:   "apply a topology and wait until it runs\nA topology already running under that name is changed in place: hot config\nlive, cold config by recreating the node.",
			flags:  []flagDoc{{"--name", "NAME", "apply it under this deployment name, next to others of the same template"}},
			examples: []string{
				`symphony-cli apply "Orchestration (saga)"`,
				`symphony-cli apply my-system.json --name "Experiment 1"`}},
		{name: "teardown", args: "<deployment>", group: "Topologies", docker: needDocker, run: cmdTeardown, changes: true,
			help:     "remove a deployment's containers and network, and forget it",
			examples: []string{`symphony-cli teardown "Orchestration (saga)"`}},
		{name: "restore", group: "Topologies", docker: needDocker, run: cmdRestore, changes: true,
			help:     "recreate stored deployments whose containers are gone; end overdue faults\nA deployment is recreated when a container of it is gone or stopped (a reboot, a\nDocker restart). The desktop app does this by itself when it starts.",
			examples: []string{`symphony-cli restore`}},

		// running deployments
		{name: "status", group: "Deployments", docker: wantDocker, run: cmdStatus,
			help:     "Docker, the state database and every deployment with its phase",
			examples: []string{`symphony-cli status`, `symphony-cli --json status`}},
		{name: "inspect", args: "<deployment> [node]", group: "Deployments", docker: needDocker, run: cmdInspect,
			help: "a deployment's nodes and edges, or one node's health checks, config and limits\nThe node and edge ids it prints are what config, resources, fault and logs take.",
			examples: []string{
				`symphony-cli inspect "Orchestration (saga)"`,
				`symphony-cli inspect "Orchestration (saga)" payment`}},
		{name: "config", args: "<deployment> <node> key=value...", group: "Deployments", docker: needDocker, run: cmdConfig, changes: true,
			help:     "change hot config keys of a running node, live\nOnly keys the node's hot config already has can be set (inspect lists them).",
			examples: []string{`symphony-cli config "Orchestration (saga)" orchestrator timeout_ms=1500 retry_max=1`}},
		{name: "resources", args: "<deployment> <node> [--cpus N] [--memory-mb N] [--pids N]", group: "Deployments", docker: needDocker, run: cmdResources, changes: true,
			help: "change a running node's CPU, memory and pid limits in place (0 = unlimited)\nWithout a flag it prints the limits.",
			flags: []flagDoc{
				{"--cpus", "N", "CPU limit in cores, e.g. 0.25"},
				{"--memory-mb", "N", "memory limit in MB (a hard limit: beyond it the container is OOM-killed)"},
				{"--pids", "N", "limit on the number of processes"}},
			examples: []string{`symphony-cli resources "Orchestration (saga)" payment --cpus 0.25 --memory-mb 128`}},
		{name: "fault", args: "<deployment> <node|edge> <kind> [--for DURATION] [key=value...]", group: "Deployments", docker: needDocker, run: cmdFault, changes: true,
			blocks: "with --for, for kill, pause, crash and a whole-node partition: until the fault ends (Ctrl-C ends it early)",
			help: "inject a fault into a node, or into the link an edge is\n" +
				"kinds: " + faultKinds() + "\n" +
				"Without --for the fault stays until `clear`. kill, pause, crash and a whole-node\n" +
				"partition do not end by themselves, so with --for symphony-cli stays until they do.\n" +
				"key=value are the kind's knobs, e.g. ms=250 for latency, rate=0.2 for drop.",
			flags: []flagDoc{{"--for", "DURATION", "end the fault after this long, e.g. 10s or 1m30s (default: until cleared)"}},
			examples: []string{
				`symphony-cli fault "Orchestration (saga)" e-proxy-pay drop rate=0.3 --for 20s`,
				`symphony-cli fault "Orchestration (saga)" inventory pause --for 10s`,
				`symphony-cli fault "Orchestration (saga)" payment latency ms=250 jitter_ms=50`}},
		{name: "clear", args: "<deployment> <node|edge>", group: "Deployments", docker: needDocker, run: cmdClear, changes: true,
			help:     "clear every fault on a node, whoever injected it\nGiven an edge, it clears the node that sends on it.",
			examples: []string{`symphony-cli clear "Orchestration (saga)" payment`}},
		{name: "load", args: "<deployment> [stop | key=value...] [--node GATEWAY]", group: "Deployments", docker: needDocker, run: cmdLoad, changes: true,
			help: "drive load through the gateway, stop it, or (no arguments) show it\n" +
				"keys: rps, pattern (constant|ramp|burst|sine|step), to_rps, factor, burst_ms,\n" +
				"period_ms, step_every_ms, step_rps, duration_ms (0 = until stopped), concurrency,\n" +
				"payload_bytes. A load without an end is restarted when the deployment is reopened.",
			flags: []flagDoc{{"--node", "GATEWAY", "the gateway to use when the topology has several"}},
			examples: []string{
				`symphony-cli load "Orchestration (saga)" rps=40 concurrency=32`,
				`symphony-cli load "Orchestration (saga)" pattern=ramp rps=10 to_rps=200 duration_ms=60000`,
				`symphony-cli load "Orchestration (saga)"`,
				`symphony-cli load "Orchestration (saga)" stop`}},
		{name: "control", args: "<deployment> <node> <op> [key=value...]", group: "Deployments", docker: needDocker, run: cmdControl, changes: true,
			help: "send a raw /control command to every replica of a node\n" +
				"The first replica's answer is printed, as JSON. The op `ops` lists what a node understands; config.get, fault.list, sagas.summary,\n" +
				"sagas.recent, behaviour.get and topics.get only read.",
			examples: []string{
				`symphony-cli control "Orchestration (saga)" gateway sagas.summary`,
				`symphony-cli control "Orchestration (saga)" payment ops`}},
		{name: "logs", args: "<deployment> <node> [-n LINES]", group: "Deployments", docker: needDocker, run: cmdLogs,
			help:     "a node's recent log lines",
			flags:    []flagDoc{{"-n", "LINES", "how many lines (default 100)"}},
			examples: []string{`symphony-cli logs "Orchestration (saga)" orchestrator -n 50`}},
		{name: "top", args: "[deployment] [--count N]", group: "Deployments", docker: needDocker, run: cmdTop,
			blocks: "until Ctrl-C, unless --count is given",
			help:   "live run metrics, one line a second, for one deployment or all of them\nsagas/s, p50 and p99 latency, failure rate, consumer lag, CPU, active faults.",
			flags:  []flagDoc{{"--count", "N", "print N lines and return (default: until Ctrl-C)"}},
			examples: []string{
				`symphony-cli top "Orchestration (saga)"`,
				`symphony-cli --json top "Orchestration (saga)" --count 5`}},
		{name: "ps", args: "[-a]", group: "Deployments", docker: needDocker, run: cmdPs,
			help:     "every container on the machine, like docker ps, with Symphony's own marked",
			flags:    []flagDoc{{"-a", "", "include the stopped ones"}},
			examples: []string{`symphony-cli ps`}},
		{name: "log", args: "[--for DURATION]", group: "Deployments", docker: needDocker, run: cmdLog,
			blocks: "until Ctrl-C, unless --for is given",
			help:   "follow what every container of Symphony prints, and the engine's own log",
			flags:  []flagDoc{{"--for", "DURATION", "follow for this long and return, e.g. 10s (default: until Ctrl-C)"}},
			examples: []string{
				`symphony-cli log`,
				`symphony-cli log --for 10s`}},

		// tests
		{name: "scenarios", group: "Tests", run: cmdScenarios,
			help:     "the predefined test scenarios, with their parameters",
			examples: []string{`symphony-cli scenarios`}},
		{name: "test", args: "<scenario> <template|file> [--name NAME] [--report-md FILE] [--report-pdf FILE] [param=value...]", group: "Tests", docker: needDocker, run: cmdTest, changes: true,
			blocks: "until the run ends (a scenario's duration_s, a minute by default); Ctrl-C aborts it",
			help: "run a scenario against a template and wait for the verdict\n" +
				"The template is applied first when it is not running. Exits 3 when the run does\n" +
				"not pass. The deployment is returned to the template afterwards and keeps running.",
			flags: []flagDoc{
				{"--name", "NAME", "run it on the deployment of this name"},
				{"--report-md", "FILE", "write the run's report there as Markdown: verdict, topology, numbers per phase, timeline, data"},
				{"--report-pdf", "FILE", "write the report there as PDF, with a picture of the topology and a chart of every series"}},
			examples: []string{
				`symphony-cli test t04-crash "Choreography (saga)"`,
				`symphony-cli test t04-crash "Choreography (saga)" --report-md crash.md --report-pdf crash.pdf`,
				`symphony-cli --json test t01-baseline "Orchestration (saga)" rps=80 duration_s=30`}},
		{name: "runs", group: "Tests", run: cmdRuns,
			help:     "stored test runs, newest first",
			examples: []string{`symphony-cli runs`}},
		{name: "run", args: "<id> [--report-md FILE] [--report-pdf FILE]", group: "Tests", run: cmdRun,
			help: "one run's phases, summary and assertions (with --json: its series too)\n" +
				"With --report-md or --report-pdf it writes the stored run's report instead.",
			flags: []flagDoc{
				{"--report-md", "FILE", "write the run's report there as Markdown"},
				{"--report-pdf", "FILE", "write the run's report there as PDF"}},
			examples: []string{
				`symphony-cli run t04-crash-choreography-saga-1790000000`,
				`symphony-cli run t04-crash-choreography-saga-1790000000 --report-pdf crash.pdf`}},
		{name: "compare", args: "<id> <id>", group: "Tests", run: cmdCompare,
			help:     "two runs' summaries side by side",
			examples: []string{`symphony-cli compare <run on one template> <run on the other>`}},
		{name: "rm-run", args: "<id>", group: "Tests", run: cmdRmRun, changes: true,
			help:     "delete a stored run",
			examples: []string{`symphony-cli rm-run t04-crash-choreography-saga-1790000000`}},

		// help
		{name: "help", args: "[command]", group: "Help", standalone: true, run: cmdHelp,
			help:     "this list, or one command explained\n`symphony-cli <command> --help` is the same as `symphony-cli help <command>`.",
			examples: []string{`symphony-cli help`, `symphony-cli help fault`}},
		{name: "generate-guide", args: "[--out FILE]", group: "Help", standalone: true, run: cmdGenerateGuide,
			help: "write GUIDE.md next to the binary: workflows, examples and every command\n" +
				"The guide explains the concepts and conventions, walks through the usual workflows\n" +
				"with examples, and lists every command, fault kind and scenario. It is written for\n" +
				"people and for agentic models alike: an agent should read it (or the output of\n" +
				"`symphony-cli --json-help`) before driving the CLI.",
			flags:    []flagDoc{{"--out", "FILE", "write it there instead (- prints it)"}},
			examples: []string{`symphony-cli generate-guide`, `symphony-cli generate-guide --out -`}},
	}
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
