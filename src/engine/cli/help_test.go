package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"symphony/engine/spec"
	"symphony/engine/tests"
)

// cli runs symphony-cli in-process and returns what it printed.
func cli(args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = run(args, nil, &out, &errb)
	return code, out.String(), errb.String()
}

// Every command explains itself: a summary, at least one example that
// starts with the command, and a description of every flag its synopsis
// shows.
func TestCommandTableIsComplete(t *testing.T) {
	seen := map[string]bool{}
	flagRE := regexp.MustCompile(`\[(--?[a-z-]+)`)
	for _, c := range commands {
		if seen[c.name] {
			t.Errorf("%s is in the table twice", c.name)
		}
		seen[c.name] = true
		if c.help == "" || c.group == "" || c.run == nil {
			t.Errorf("%s: help, group and run are required", c.name)
		}
		if len(firstLine(c.help)) > 80 {
			t.Errorf("%s: the summary line is %d characters, the list shows 80", c.name, len(firstLine(c.help)))
		}
		if len(c.examples) == 0 {
			t.Errorf("%s has no example", c.name)
		}
		for _, ex := range c.examples {
			if !strings.Contains(ex, " "+c.name) || !strings.HasPrefix(ex, "symphony-cli ") {
				t.Errorf("%s: example %q does not call it", c.name, ex)
			}
		}
		documented := map[string]bool{}
		for _, f := range c.flags {
			documented[f.name] = true
			if f.help == "" {
				t.Errorf("%s %s has no description", c.name, f.name)
			}
			if !strings.Contains(c.args, f.name) {
				t.Errorf("%s: flag %s is described but not in the synopsis %q", c.name, f.name, c.args)
			}
		}
		for _, m := range flagRE.FindAllStringSubmatch(c.args, -1) {
			if !documented[m[1]] {
				t.Errorf("%s: flag %s is in the synopsis but not described", c.name, m[1])
			}
		}
	}
	for _, k := range spec.AllFaultKinds {
		if d, ok := faultDocs[k]; !ok || d.does == "" || d.by == "" {
			t.Errorf("fault kind %q is not documented", k)
		}
	}
	if len(faultDocs) != len(spec.AllFaultKinds) {
		t.Errorf("%d fault kinds documented, %d exist", len(faultDocs), len(spec.AllFaultKinds))
	}
}

func TestHelp(t *testing.T) {
	// the three spellings of the general help, and no arguments at all
	_, want, _ := cli("--help")
	for _, args := range [][]string{{"-h"}, {"help"}, {}} {
		code, out, _ := cli(args...)
		if code != exitOK || out != want {
			t.Errorf("symphony-cli %v: exit %d, and not the output of --help", args, code)
		}
	}
	for _, c := range commands {
		if !strings.Contains(want, "\n  "+synopsis(&c)+"\n") {
			t.Errorf("--help does not list %q", synopsis(&c))
		}
	}
	for _, s := range []string{"--json-help", "generate-guide", "<command> --help", "agentic models"} {
		if !strings.Contains(want, s) {
			t.Errorf("--help does not mention %q", s)
		}
	}

	// one command's help, however it is asked for, and without needing
	// Docker or a state database
	_, want, _ = cli("help", "fault")
	for _, args := range [][]string{{"fault", "--help"}, {"fault", "-h"}, {"fault", "x", "y", "--help"}, {"--json=false", "fault", "--help"}} {
		code, out, errs := cli(args...)
		if code != exitOK || out != want || errs != "" {
			t.Errorf("symphony-cli %v: exit %d, stderr %q, and not the output of help fault", args, code, errs)
		}
	}
	for _, s := range []string{"usage: symphony-cli fault <deployment>", "--for DURATION", "examples:", "drop rate=0.3", "changes state", "needs the Docker daemon"} {
		if !strings.Contains(want, s) {
			t.Errorf("help fault lacks %q:\n%s", s, want)
		}
	}
	for _, c := range commands {
		if code, out, _ := cli(c.name, "--help"); code != exitOK || !strings.HasPrefix(out, "usage: symphony-cli "+c.name) {
			t.Errorf("%s --help: exit %d, %q", c.name, code, firstLine(out))
		}
	}

	if code, _, errs := cli("nope"); code != exitUsage || !strings.Contains(errs, "unknown command") {
		t.Errorf("an unknown command: exit %d, %q", code, errs)
	}
	if code, _, errs := cli("help", "nope"); code != exitUsage || !strings.Contains(errs, "unknown command") {
		t.Errorf("help for an unknown command: exit %d, %q", code, errs)
	}
	if code, _, errs := cli("--nope"); code != exitUsage || !strings.Contains(errs, "usage: symphony-cli") {
		t.Errorf("an unknown flag: exit %d, %q", code, errs)
	}
}

func TestJSONHelp(t *testing.T) {
	code, out, _ := cli("--json-help")
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}
	var cat catalogue
	if err := json.Unmarshal([]byte(out), &cat); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if len(cat.Commands) != len(commands) || len(cat.FaultKinds) != len(spec.AllFaultKinds) ||
		len(cat.Scenarios) != len(tests.Catalogue()) || len(cat.Templates) != len(spec.Presets()) || len(cat.ExitCodes) != 4 {
		t.Fatalf("%d commands, %d fault kinds, %d scenarios, %d templates, %d exit codes",
			len(cat.Commands), len(cat.FaultKinds), len(cat.Scenarios), len(cat.Templates), len(cat.ExitCodes))
	}
	byName := map[string]commandJSON{}
	for _, c := range cat.Commands {
		byName[c.Name] = c
		if c.Usage == "" || c.Summary == "" || len(c.Examples) == 0 || c.Flags == nil {
			t.Errorf("%s is incomplete: %+v", c.Name, c)
		}
	}
	if c := byName["apply"]; c.Docker != "required" || !c.ChangesState || c.Blocks == "" || len(c.Flags) != 1 || c.Flags[0].Name != "--name" {
		t.Errorf("apply: %+v", c)
	}
	if c := byName["runs"]; c.Docker != "no" || c.ChangesState || c.Blocks != "" {
		t.Errorf("runs: %+v", c)
	}
	if !strings.Contains(cat.Guide, "generate-guide") {
		t.Errorf("the catalogue does not point at the guide: %q", cat.Guide)
	}
	// the same document through the help command
	if _, viaHelp, _ := cli("--json", "help"); viaHelp != out {
		t.Error("--json help differs from --json-help")
	}
}

func TestGenerateGuide(t *testing.T) {
	code, guide, _ := cli("generate-guide", "--out", "-")
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}
	for _, c := range commands {
		if !strings.Contains(guide, "\n#### "+c.name+"\n") || !strings.Contains(guide, "`symphony-cli "+synopsis(&c)+"`") {
			t.Errorf("the guide does not document %s", c.name)
		}
		for _, ex := range c.examples {
			if !strings.Contains(guide, ex) {
				t.Errorf("the guide lacks the example %q", ex)
			}
		}
	}
	for _, k := range spec.AllFaultKinds {
		if !strings.Contains(guide, "| `"+string(k)+"`") {
			t.Errorf("the guide does not list the fault kind %s", k)
		}
	}
	for _, s := range tests.Catalogue() {
		if !strings.Contains(guide, "**`"+s.ID+"`**") {
			t.Errorf("the guide does not list the scenario %s", s.ID)
		}
	}
	for _, p := range spec.Presets() {
		if !strings.Contains(guide, "### "+p.Name+"\n") {
			t.Errorf("the guide does not describe the template %s", p.Name)
		}
	}
	for _, s := range []string{"## For agentic models", "## Workflows", "symphony-cli --json-help", "<command> --help", "| 3 | a test ran to its end and did not pass |"} {
		if !strings.Contains(guide, s) {
			t.Errorf("the guide lacks %q", s)
		}
	}
	if strings.Contains(guide, "<no value>") || strings.Contains(guide, "{{") {
		t.Error("the template left something unrendered")
	}

	// to a file
	path := filepath.Join(t.TempDir(), "sub.md")
	code, out, errs := cli("generate-guide", "--out", path)
	if code != exitOK || !strings.Contains(out, "wrote "+path) {
		t.Fatalf("exit %d, %q, %q", code, out, errs)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != guide {
		t.Fatalf("the file differs from what --out - prints (%v)", err)
	}
	// the default is next to the binary
	if p, err := guidePath(); err != nil || filepath.Base(p) != GuideName {
		t.Fatalf("guidePath: %q, %v", p, err)
	}
	if code, _, _ := cli("generate-guide", "extra"); code != exitUsage {
		t.Errorf("a stray argument: exit %d", code)
	}
}
