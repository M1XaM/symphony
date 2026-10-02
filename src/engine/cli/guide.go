package cli

import (
	"bytes"
	_ "embed"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
)

// GuideName is the file generate-guide writes.
const GuideName = "GUIDE.md"

//go:embed guide.md.tmpl
var guideTemplate string

// groupJSON is one group of commands, for the guide's reference section.
type groupJSON struct {
	Name     string
	Commands []commandJSON
}

// renderGuide writes the guide from the catalogue: its prose is in
// guide.md.tmpl, everything that could drift (the commands, their flags and
// examples, the fault kinds, the scenarios, the templates) comes from the
// tables the CLI itself runs on.
func renderGuide() ([]byte, error) {
	cat := buildCatalogue()
	var grouped []groupJSON
	for _, g := range groups() {
		grp := groupJSON{Name: g}
		for _, c := range cat.Commands {
			if c.Group == g {
				grp.Commands = append(grp.Commands, c)
			}
		}
		grouped = append(grouped, grp)
	}
	funcs := template.FuncMap{
		"join": strings.Join,
		// a command's summary as a sentence, and what its help says after it
		"sentence": func(s string) string {
			if s == "" {
				return s
			}
			return strings.ToUpper(s[:1]) + s[1:] + "."
		},
		"details": func(help string) string {
			_, rest, _ := strings.Cut(help, "\n")
			return rest
		},
		"params": func(m map[string]any) string {
			if len(m) == 0 {
				return "none"
			}
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			parts := make([]string, len(keys))
			for i, k := range keys {
				parts[i] = fmt.Sprintf("`%s=%s`", k, fmtValue(m[k]))
			}
			return strings.Join(parts, " ")
		},
	}
	t, err := template.New("guide").Funcs(funcs).Parse(guideTemplate)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	err = t.Execute(&buf, struct {
		catalogue
		Groups []groupJSON
	}{cat, grouped})
	return buf.Bytes(), err
}

// guidePath is GUIDE.md in the directory of the running binary.
func guidePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate the binary: %w", err)
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return filepath.Join(filepath.Dir(exe), GuideName), nil
}

func cmdGenerateGuide(e *env, args []string) error {
	fset := flag.NewFlagSet("generate-guide", flag.ContinueOnError)
	out := fset.String("out", "", "")
	pos, err := parse(fset, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("takes no arguments")
	}
	guide, err := renderGuide()
	if err != nil {
		return err
	}
	if *out == "-" {
		_, err := e.out.Write(guide)
		return err
	}
	path := *out
	if path == "" {
		if path, err = guidePath(); err != nil {
			return err
		}
	}
	if err := os.WriteFile(path, guide, 0o644); err != nil {
		if *out == "" {
			return fmt.Errorf("%w (the binary's directory cannot be written: give a file with --out, or --out - to print the guide)", err)
		}
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	fmt.Fprintf(e.out, "wrote %s\n", abs)
	return nil
}
