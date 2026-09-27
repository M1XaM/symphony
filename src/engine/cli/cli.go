// Package cli is symphony-cli: Symphony's engine, state database and Docker
// containers driven from a terminal instead of the window. Every command
// goes through the same api.API the webview calls and the same symphony.db
// (next to the binary), so the two work side by side: the desktop app picks
// up what the CLI changes within a few seconds, and the CLI sees whatever
// the app applied.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"text/tabwriter"
	"time"

	"symphony/engine/api"
	"symphony/engine/backend"
	"symphony/engine/events"
)

// Exit codes.
const (
	exitOK     = 0
	exitError  = 1
	exitUsage  = 2
	exitFailed = 3 // a test ran and did not pass
)

// docker says what a command needs from the daemon.
type docker int

const (
	noDocker   docker = iota // works on the stored state alone
	wantDocker               // uses it when it answers
	needDocker               // fails without it
)

type command struct {
	name   string
	args   string
	help   string
	group  string
	docker docker
	run    func(e *env, args []string) error
}

// env is what a command runs with.
type env struct {
	ctx     context.Context
	b       *backend.Backend
	a       *api.API
	out     io.Writer
	json    bool
	verbose bool
	docker  events.RuntimeStatusPayload
	// echo prints warnings and errors from the engine log while a command
	// runs (every line with -v); the log command prints the log itself
	echo atomic.Bool
}

// usageError makes Main print the command's synopsis and exit 2.
type usageError struct{ msg string }

func (u usageError) Error() string { return u.msg }

func usagef(format string, args ...any) error { return usageError{fmt.Sprintf(format, args...)} }

// errFailed is returned by a command whose outcome, not its execution,
// failed (a test that did not pass); the command has said why already.
var errFailed = errors.New("failed")

// Main runs symphony-cli with the arguments after the program name and
// returns the exit code. blocks is the embedded source the block images are
// built from.
func Main(args []string, blocks fs.FS) int {
	global := flag.NewFlagSet("symphony-cli", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	db := global.String("db", "", "")
	asJSON := global.Bool("json", false, "")
	verbose := global.Bool("v", false, "")
	if err := global.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(os.Stdout)
			return exitOK
		}
		fmt.Fprintln(os.Stderr, "symphony-cli:", err)
		usage(os.Stderr)
		return exitUsage
	}
	rest := global.Args()
	if len(rest) == 0 || rest[0] == "help" || rest[0] == "--help" {
		if len(rest) > 1 {
			if c := find(rest[1]); c != nil {
				fmt.Printf("usage: symphony-cli %s %s\n\n%s\n", c.name, c.args, c.help)
				return exitOK
			}
		}
		usage(os.Stdout)
		return exitOK
	}
	cmd := find(rest[0])
	if cmd == nil {
		fmt.Fprintf(os.Stderr, "symphony-cli: unknown command %q (see symphony-cli help)\n", rest[0])
		return exitUsage
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	b, err := backend.New(backend.Options{Blocks: blocks, DBPath: *db})
	if err != nil {
		fmt.Fprintln(os.Stderr, "symphony-cli:", err)
		return exitError
	}
	defer b.Close()
	api.Startup(b.API, ctx, nil)
	e := &env{ctx: ctx, b: b, a: b.API, out: os.Stdout, json: *asJSON, verbose: *verbose}

	if cmd.docker != noDocker {
		b.Connect(ctx, false)
	}
	e.docker = b.API.GetRuntimeStatus()
	if cmd.docker == needDocker && !e.docker.Available {
		fmt.Fprintf(os.Stderr, "symphony-cli: Docker is not available: install Docker and start it, then try again\n  %s\n", e.docker.Detail)
		return exitError
	}
	e.echo.Store(true)
	b.Bus.Subscribe(func(ev events.Event) {
		l, ok := ev.Payload.(events.LogPayload)
		if !ok || !e.echo.Load() {
			return
		}
		// the fault commands say what they injected themselves
		if e.verbose || l.Level == "error" || (l.Level == "warn" && l.Source != "fault") {
			fmt.Fprintln(os.Stderr, logLine(l))
		}
	})

	err = cmd.run(e, rest[1:])
	var ue usageError
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, errFailed):
		return exitFailed
	case errors.As(err, &ue):
		fmt.Fprintf(os.Stderr, "symphony-cli %s: %s\nusage: symphony-cli %s %s\n", cmd.name, ue.msg, cmd.name, cmd.args)
		return exitUsage
	case ctx.Err() != nil:
		fmt.Fprintln(os.Stderr, "interrupted")
		return exitError
	default:
		fmt.Fprintf(os.Stderr, "symphony-cli %s: %v\n", cmd.name, err)
		return exitError
	}
}

func find(name string) *command {
	for i := range commands {
		if commands[i].name == name {
			return &commands[i]
		}
	}
	return nil
}

func usage(w io.Writer) {
	fmt.Fprint(w, `symphony-cli drives Symphony from a terminal: the same engine, state and
Docker containers as the desktop app, which picks up what it changes.

usage: symphony-cli [--db FILE] [--json] [-v] <command> [arguments]

  --db FILE   state database (default: symphony.db next to the binary, or $SYMPHONY_DB)
  --json      print results as JSON
  -v          print every engine log line, not only warnings and errors
`)
	group := ""
	for _, c := range commands {
		if c.group != group {
			group = c.group
			fmt.Fprintf(w, "\n%s\n", group)
		}
		fmt.Fprintf(w, "  %s\n      %s\n", strings.TrimSpace(c.name+" "+c.args), firstLine(c.help))
	}
	fmt.Fprint(w, "\nsymphony-cli help <command> explains one command.\n")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// --- arguments ---------------------------------------------------------------

// parse reads a command's flags wherever they appear among its arguments and
// returns the rest in order.
func parse(fset *flag.FlagSet, args []string) ([]string, error) {
	fset.SetOutput(io.Discard)
	var pos []string
	for {
		if err := fset.Parse(args); err != nil {
			return nil, usagef("%v", err)
		}
		args = fset.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// keyValues turns key=value arguments into a map. A value that parses as
// JSON (a number, true, an object, a quoted string) keeps that type; any
// other value is a string.
func keyValues(args []string) (map[string]any, error) {
	out := make(map[string]any, len(args))
	for _, a := range args {
		k, v, ok := strings.Cut(a, "=")
		if !ok || k == "" {
			return nil, usagef("%q is not key=value", a)
		}
		var val any
		if err := json.Unmarshal([]byte(v), &val); err != nil {
			val = v
		}
		out[k] = val
	}
	return out, nil
}

// --- output ------------------------------------------------------------------

// table prints rows under a header, aligned.
func (e *env) table(header string, rows [][]string) {
	tw := tabwriter.NewWriter(e.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, header)
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	tw.Flush()
}

func (e *env) printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(e.out, string(b))
	return err
}

func logLine(l events.LogPayload) string {
	return fmt.Sprintf("%s %-5s %-10s %s", time.UnixMilli(l.Ts).Format("15:04:05"), l.Level, l.Source, l.Message)
}

// follow prints plan progress (and image build output) of one deployment
// until the returned function is called.
func (e *env) follow(deployment string) func() {
	return e.b.Bus.Subscribe(func(ev events.Event) {
		switch p := ev.Payload.(type) {
		case events.ProgressPayload:
			if p.Deployment != deployment || p.Status == "started" {
				return
			}
			line := fmt.Sprintf("  [%2d/%d] %-16s %-26s %s", p.Index+1, p.Count, p.Kind, p.Target, p.Status)
			if p.Elapsed > 0 {
				line += fmt.Sprintf(" (%s)", fmtElapsed(time.Duration(p.Elapsed)*time.Millisecond))
			}
			if p.Message != "" {
				line += ": " + p.Message
			}
			fmt.Fprintln(e.out, line)
		case events.LogPayload:
			if p.Source == "image" && !e.verbose {
				fmt.Fprintln(e.out, "  "+p.Message)
			}
		}
	})
}

func fmtElapsed(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return d.Round(100 * time.Millisecond).String()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func fmtValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return fmt.Sprintf("%g", x)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func fmtBytes(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MiB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KiB", n>>10)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
