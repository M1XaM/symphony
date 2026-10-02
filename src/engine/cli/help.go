// Help in its three forms, all written from the command table: the text of
// `symphony-cli --help` and `<command> --help`, the machine-readable
// catalogue of `--json-help`, and (in guide.go) the generated GUIDE.md.

package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"symphony/engine/spec"
	"symphony/engine/tests"
)

// globalFlags are the flags that go before the command.
var globalFlags = []flagDoc{
	{"--db", "FILE", "state database (default: symphony.db next to the binary, or $SYMPHONY_DB)"},
	{"--json", "", "print the result as JSON instead of text"},
	{"-v", "", "print every engine log line, not only warnings and errors"},
	{"--help", "", "this help; after a command, that command's help"},
	{"--json-help", "", "everything here, and the fault kinds, scenarios and templates, as one JSON document (for scripts and agentic models)"},
}

// exitCodes are what a script can tell apart.
var exitCodes = []struct {
	code    int
	meaning string
}{
	{exitOK, "the command did what it was asked"},
	{exitError, "it failed (the reason is on stderr)"},
	{exitUsage, "it was called wrongly (the usage is on stderr)"},
	{exitFailed, "a test ran to its end and did not pass"},
}

// faultDoc explains one fault kind: who carries it out and its knobs.
type faultDoc struct {
	by     string
	params string
	does   string
}

// faultDocs covers every spec.FaultKind (a test keeps it complete).
var faultDocs = map[spec.FaultKind]faultDoc{
	spec.FaultKill:      {"Docker", "", "SIGKILL the node's containers; they are started again when the fault ends"},
	spec.FaultPause:     {"Docker", "", "freeze the node's processes"},
	spec.FaultPartition: {"Docker, or the block with peers", `peers=["node",...]`, "cut the node off the network; with peers, refuse traffic to and from those nodes only"},
	spec.FaultCPU:       {"the block", "cores=1", "burn CPU inside the container (10s unless --for says otherwise)"},
	spec.FaultMemory:    {"the block", "mb=100", "allocate and hold memory inside the container"},
	spec.FaultStall:     {"the block", "", "freeze the data path while the admin port keeps answering (5s unless --for says otherwise)"},
	spec.FaultCrash:     {"the block", "exit_code=137", "the process exits; it is started again when the fault ends"},
	spec.FaultLatency:   {"the block, per request", "ms=100 jitter_ms=0 rate=1", "delay requests"},
	spec.FaultDrop:      {"the block, per request", "rate=1", "swallow a share of the requests or messages"},
	spec.FaultError:     {"the block, per request", "rate=1 status=503", "answer a share of the requests with an error"},
	spec.FaultTimeout:   {"the block, per request", "rate=1", "hang until the caller gives up"},
	spec.FaultSlow:      {"the block, per request", "bytes_per_sec=1024", "throttle the response body"},
	spec.FaultCorrupt:   {"the block, per request", "rate=1", "answer with a malformed body"},
	spec.FaultDuplicate: {"the block, per request", "rate=1", "deliver a share of the requests twice"},
	spec.FaultReorder:   {"the broker", "", "deliver messages out of order (on a subscribes edge)"},
	spec.FaultReject:    {"the block, per request", "rate=1", "refuse connections"},
}

func synopsis(c *command) string {
	return strings.TrimSpace(c.name + " " + c.args)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func dockerNeed(d docker) string {
	switch d {
	case needDocker:
		return "required"
	case wantDocker:
		return "optional"
	}
	return "no"
}

func flagLine(f flagDoc) string {
	return strings.TrimSpace(f.name + " " + f.value)
}

// groups lists the command groups in table order.
func groups() []string {
	var out []string
	for _, c := range commands {
		if len(out) == 0 || out[len(out)-1] != c.group {
			out = append(out, c.group)
		}
	}
	return out
}

// usage is `symphony-cli --help`.
func usage(w io.Writer) {
	fmt.Fprint(w, `symphony-cli drives Symphony from a terminal: the same engine, state and
Docker containers as the desktop app, which picks up what it changes.

usage: symphony-cli [--db FILE] [--json] [-v] <command> [arguments]

`)
	for _, f := range globalFlags {
		fmt.Fprintf(w, "  %-12s %s\n", flagLine(f), f.help)
	}
	group := ""
	for _, c := range commands {
		if c.group != group {
			group = c.group
			fmt.Fprintf(w, "\n%s\n", group)
		}
		fmt.Fprintf(w, "  %s\n      %s\n", synopsis(&c), firstLine(c.help))
	}
	fmt.Fprint(w, `
A <template> is a built-in or saved template's name, a running deployment's
name, or a topology JSON file; a <deployment> is the name it was applied under.

symphony-cli <command> --help   explains one command, with examples
symphony-cli generate-guide     writes GUIDE.md next to the binary: workflows,
                                examples and every command, for people and for
                                agentic models
symphony-cli --json-help        the same catalogue as JSON
`)
}

// commandHelp is `symphony-cli <command> --help`.
func commandHelp(w io.Writer, c *command) {
	fmt.Fprintf(w, "usage: symphony-cli %s\n\n%s\n", synopsis(c), c.help)
	if len(c.flags) > 0 {
		fmt.Fprintln(w, "\nflags:")
		for _, f := range c.flags {
			fmt.Fprintf(w, "  %-18s %s\n", flagLine(f), f.help)
		}
	}
	if len(c.examples) > 0 {
		fmt.Fprintln(w, "\nexamples:")
		for _, ex := range c.examples {
			fmt.Fprintf(w, "  %s\n", ex)
		}
	}
	fmt.Fprintln(w)
	switch c.docker {
	case needDocker:
		fmt.Fprintln(w, "needs the Docker daemon")
	case wantDocker:
		fmt.Fprintln(w, "uses the Docker daemon when it answers")
	}
	if c.changes {
		fmt.Fprintln(w, "changes state")
	} else {
		fmt.Fprintln(w, "only reads")
	}
	if c.blocks != "" {
		fmt.Fprintf(w, "does not return at once: %s\n", c.blocks)
	}
	fmt.Fprintln(w, "\nglobal flags (before the command): --db FILE, --json, -v; see symphony-cli --help")
}

func cmdHelp(e *env, args []string) error {
	if len(args) == 0 {
		if e.json {
			return e.printJSON(buildCatalogue())
		}
		usage(e.out)
		return nil
	}
	c := find(args[0])
	if c == nil {
		return usagef("unknown command %q", args[0])
	}
	if e.json {
		return e.printJSON(describe(c))
	}
	commandHelp(e.out, c)
	return nil
}

// --- the JSON catalogue --------------------------------------------------------

type flagJSON struct {
	Name        string `json:"name"`
	Value       string `json:"value,omitempty"`
	Description string `json:"description"`
}

type commandJSON struct {
	Name        string     `json:"name"`
	Group       string     `json:"group"`
	Usage       string     `json:"usage"`
	Summary     string     `json:"summary"`
	Description string     `json:"description"`
	Flags       []flagJSON `json:"flags"`
	Examples    []string   `json:"examples"`
	// Docker is "required", "optional" or "no".
	Docker       string `json:"docker"`
	ChangesState bool   `json:"changes_state"`
	// Blocks says when the command does not return at once; "" if it does.
	Blocks string `json:"blocks,omitempty"`
}

type exitCodeJSON struct {
	Code    int    `json:"code"`
	Meaning string `json:"meaning"`
}

type faultJSON struct {
	Kind        string `json:"kind"`
	CarriedOut  string `json:"carried_out_by"`
	Params      string `json:"params,omitempty"`
	Description string `json:"description"`
	NeedsRevert bool   `json:"stays_until_ended"`
}

type scenarioJSON struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Reveals     string         `json:"reveals,omitempty"`
	Params      map[string]any `json:"params"`
}

type templateJSON struct {
	Name  string   `json:"name"`
	Nodes []string `json:"nodes"`
	Edges []string `json:"edges"`
}

type catalogue struct {
	Program     string            `json:"program"`
	Description string            `json:"description"`
	Usage       string            `json:"usage"`
	Guide       string            `json:"guide"`
	GlobalFlags []flagJSON        `json:"global_flags"`
	ExitCodes   []exitCodeJSON    `json:"exit_codes"`
	Arguments   map[string]string `json:"arguments"`
	Commands    []commandJSON     `json:"commands"`
	FaultKinds  []faultJSON       `json:"fault_kinds"`
	Scenarios   []scenarioJSON    `json:"scenarios"`
	Templates   []templateJSON    `json:"builtin_templates"`
}

func flagsJSON(fs []flagDoc) []flagJSON {
	out := make([]flagJSON, 0, len(fs))
	for _, f := range fs {
		out = append(out, flagJSON{Name: f.name, Value: f.value, Description: f.help})
	}
	return out
}

func describe(c *command) commandJSON {
	ex := c.examples
	if ex == nil {
		ex = []string{}
	}
	return commandJSON{Name: c.name, Group: c.group, Usage: "symphony-cli " + synopsis(c), Summary: firstLine(c.help), Description: c.help,
		Flags: flagsJSON(c.flags), Examples: ex, Docker: dockerNeed(c.docker), ChangesState: c.changes, Blocks: c.blocks}
}

// buildCatalogue is everything a script or an agentic model needs to know
// to drive the CLI, from the same table the text help is printed from.
func buildCatalogue() catalogue {
	cat := catalogue{
		Program:     "symphony-cli",
		Description: "Drives Symphony from a terminal: topologies of microservice building blocks run as real Docker containers, with load, fault injection and predefined test scenarios, to compare orchestration with choreography.",
		Usage:       "symphony-cli [--db FILE] [--json] [-v] <command> [arguments]",
		Guide:       "symphony-cli generate-guide writes GUIDE.md next to the binary (generate-guide --out - prints it): concepts, conventions and workflows with examples",
		GlobalFlags: flagsJSON(globalFlags),
		Arguments: map[string]string{
			"template":   "a built-in or saved template's name, a running deployment's name, or a topology JSON file (- reads stdin)",
			"deployment": "the name a topology was applied under (the template's name unless --name was given)",
			"node":       "a node id of the deployment (symphony-cli inspect <deployment> lists them)",
			"edge":       "an edge id of the deployment; a fault on an edge hits that one link",
			"key=value":  "the value is read as JSON when it parses (a number, true, [\"a\"]), else as a string",
		},
	}
	for _, x := range exitCodes {
		cat.ExitCodes = append(cat.ExitCodes, exitCodeJSON{x.code, x.meaning})
	}
	for i := range commands {
		cat.Commands = append(cat.Commands, describe(&commands[i]))
	}
	for _, k := range spec.AllFaultKinds {
		d := faultDocs[k]
		cat.FaultKinds = append(cat.FaultKinds, faultJSON{Kind: string(k), CarriedOut: d.by, Params: d.params, Description: d.does,
			NeedsRevert: spec.FaultSpec{Kind: k}.NeedsRevert()})
	}
	for _, s := range tests.Catalogue() {
		cat.Scenarios = append(cat.Scenarios, scenarioJSON{ID: s.ID, Name: s.Name, Description: s.Description, Reveals: s.Reveals, Params: s.Params})
	}
	for _, t := range spec.Presets() {
		tj := templateJSON{Name: t.Name}
		for _, n := range t.Nodes {
			tj.Nodes = append(tj.Nodes, fmt.Sprintf("%s (%s)", n.ID, n.Kind))
		}
		for _, e := range t.Edges {
			tj.Edges = append(tj.Edges, fmt.Sprintf("%s: %s -%s-> %s", e.ID, e.From, e.Kind, e.To))
		}
		cat.Templates = append(cat.Templates, tj)
	}
	return cat
}

// printJSONHelp is `symphony-cli --json-help`.
func printJSONHelp(stdout, stderr io.Writer) int {
	b, err := json.MarshalIndent(buildCatalogue(), "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, "symphony-cli:", err)
		return exitError
	}
	fmt.Fprintln(stdout, string(b))
	return exitOK
}
