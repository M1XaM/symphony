// The test commands: scenarios, test, and the stored runs.

package cli

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"symphony/engine/events"
	"symphony/engine/report"
	"symphony/engine/tests"
)

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
	md := fset.String("report-md", "", "")
	pdf := fset.String("report-pdf", "", "")
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
	// events arrive on whichever goroutine published them
	var lastProgress atomic.Int64
	defer e.b.Bus.Subscribe(func(ev events.Event) {
		switch p := ev.Payload.(type) {
		case events.TestProgressPayload:
			last := lastProgress.Load()
			if p.Scenario != scenario || p.Template != t.Name || time.Since(time.UnixMilli(last)) < 2*time.Second ||
				!lastProgress.CompareAndSwap(last, time.Now().UnixMilli()) {
				return
			}
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
	// the reports are written whatever the verdict: a failed run is the one
	// most worth reading
	saved, err := writeReports(run, *md, *pdf)
	if err != nil {
		return err
	}
	if e.json {
		if err := e.printJSON(run); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(e.out)
		printRun(e, run)
		for _, path := range saved {
			fmt.Fprintf(e.out, "\nreport: %s", path)
		}
		if len(saved) > 0 {
			fmt.Fprintln(e.out)
		}
	}
	if !run.Passed {
		return errFailed
	}
	return nil
}

// writeReports writes a run's report as Markdown and as PDF, to the files
// named ("" skips one), and returns the files it wrote.
func writeReports(run tests.Run, md, pdf string) ([]string, error) {
	var saved []string
	for _, out := range []struct{ format, path string }{{report.FormatMarkdown, md}, {report.FormatPDF, pdf}} {
		if out.path == "" {
			continue
		}
		doc, err := report.Render(run, out.format)
		if err != nil {
			return saved, err
		}
		if err := os.WriteFile(out.path, doc, 0o644); err != nil {
			return saved, err
		}
		saved = append(saved, out.path)
	}
	return saved, nil
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
	fset := flag.NewFlagSet("run", flag.ContinueOnError)
	md := fset.String("report-md", "", "")
	pdf := fset.String("report-pdf", "", "")
	args, err := parse(fset, args)
	if err != nil {
		return err
	}
	if len(args) != 1 {
		return usagef("needs a run id")
	}
	run, err := e.a.GetRun(args[0])
	if err != nil {
		return err
	}
	saved, err := writeReports(run, *md, *pdf)
	if err != nil {
		return err
	}
	if len(saved) > 0 {
		// asked for the report: say where it is rather than print the run
		for _, path := range saved {
			fmt.Fprintf(e.out, "report: %s\n", path)
		}
		return nil
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
