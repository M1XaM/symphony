// Package report turns a stored test run into a document: everything the
// run recorded (the verdict, the assertions, the scenario and its
// parameters, the topology it ran on, the numbers per phase, every series
// as a chart, what happened when) laid out once in a Report, and rendered
// from that as Markdown or as PDF. The Markdown is text, lists and tables;
// the PDF adds the picture of the topology and a chart of every series.
package report

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"symphony/engine/spec"
	"symphony/engine/tests"
)

// Formats a report can be written in.
const (
	FormatMarkdown = "md"
	FormatPDF      = "pdf"
)

// KV is one labelled value.
type KV struct{ Key, Value string }

// Assertion is one of the scenario's checks, as evaluated.
type Assertion struct {
	Metric, Expected, Phase, Actual, Note string
	Passed                                bool
}

// Phase is a named window of the run, in seconds since it started.
type Phase struct {
	Name       string
	Start, End float64
}

// Event is something the runner did, and when.
type Event struct {
	T    float64
	Text string
	// Fault marks the events drawn on the charts: a fault, a kill.
	Fault bool
}

// Series is one line of a chart.
type Series struct {
	Name   string
	Points []tests.Point
}

// Chart is one or more series over the run's time axis.
type Chart struct {
	Title, Unit string
	Series      []Series
}

// NodeRow describes a node and, when the run measured it, its peaks.
type NodeRow struct {
	ID, Kind, Image   string
	Replicas          int
	Limits, Hot, Cold string
	Measured          bool
	PeakCPU, PeakMem  float64 // percent of a core, MB
}

// EdgeRow describes an edge and, when the run measured it, its peaks.
type EdgeRow struct {
	ID, From, To, Kind, Topic string
	TimeoutMs, Retries        int
	// Measured: the run recorded the link's failure rate; RateMeasured: its
	// request rate too (a scenario chooses what it collects)
	Measured, RateMeasured bool
	PeakError, PeakRPS     float64 // share 0..1, requests per second
}

// Report is a run laid out for reading.
type Report struct {
	RunID, Scenario, ScenarioName, Template string
	Description, Reveals                    string
	Started, Ended                          time.Time
	Duration                                float64 // seconds
	Verdict                                 string  // PASSED | FAILED | ABORTED
	Error                                   string
	Params                                  []KV
	Assertions                              []Assertion
	Phases                                  []Phase
	Timeline                                []Event
	KeyNumbers                              []KV
	// PhaseTable: one row per headline metric, one column per phase (the
	// mean over that phase), "-" where the phase has no sample.
	PhaseColumns []string
	PhaseRows    [][]string
	Charts       []Chart
	Topology     spec.Topology
	Nodes        []NodeRow
	Edges        []EdgeRow
	Summary      []KV
	// Data: the headline series sampled at regular steps, as a table.
	DataColumns []string
	DataRows    [][]string
}

// headline are the series every run is summarised by, with how they read.
var headline = []struct{ series, label, unit string }{
	{"sagas_per_sec", "Throughput", "sagas/s"},
	{"latency_p50_ms", "Latency p50", "ms"},
	{"latency_p99_ms", "Latency p99", "ms"},
	{"error_rate", "Failed", "%"},
	{"consumer_lag", "Consumer lag", "msgs"},
	{"cpu_pct", "CPU", "% of a core"},
}

// Build lays a run out. The scenario's description is looked up in the
// catalogue; a run of a scenario this build no longer has still reports.
func Build(run tests.Run) Report {
	r := Report{
		RunID: run.ID, Scenario: run.Scenario, ScenarioName: run.Name, Template: run.Template,
		Started: time.UnixMilli(run.StartedAt), Error: run.Error, Topology: run.Topology,
	}
	if run.EndedAt > 0 {
		r.Ended = time.UnixMilli(run.EndedAt)
	}
	switch {
	case run.Aborted:
		r.Verdict = "ABORTED"
	case run.Passed:
		r.Verdict = "PASSED"
	default:
		r.Verdict = "FAILED"
	}
	for _, s := range tests.Catalogue() {
		if s.ID == run.Scenario {
			r.Description, r.Reveals = s.Description, s.Reveals
		}
	}
	for _, k := range sortedKeys(run.Params) {
		r.Params = append(r.Params, KV{k, fmt.Sprint(run.Params[k])})
	}
	for _, a := range run.Assertions {
		r.Assertions = append(r.Assertions, Assertion{Metric: a.Metric, Expected: a.Op + " " + num(a.Value), Phase: a.Phase,
			Actual: num(a.Actual), Note: a.Note, Passed: a.Passed})
	}
	for _, p := range run.Phases {
		r.Phases = append(r.Phases, Phase{p.Name, p.Start, p.End})
		r.Duration = math.Max(r.Duration, p.End)
	}
	for _, pts := range run.Series {
		if n := len(pts); n > 0 {
			r.Duration = math.Max(r.Duration, pts[n-1].T)
		}
	}
	r.Timeline = timeline(run.Notes)
	for _, e := range r.Timeline {
		r.Duration = math.Max(r.Duration, e.T)
	}
	r.KeyNumbers = keyNumbers(run.Summary)
	r.PhaseColumns, r.PhaseRows = phaseTable(run)
	r.Charts = charts(run)
	r.Nodes, r.Edges = rows(run)
	for _, k := range sortedKeys(run.Summary) {
		r.Summary = append(r.Summary, KV{k, num(run.Summary[k])})
	}
	r.DataColumns, r.DataRows = dataTable(run, r.Duration)
	return r
}

// timeline reads the runner's notes: "  12.0s fault latency on payment ...".
func timeline(notes []string) []Event {
	var out []Event
	for _, n := range notes {
		at, text, ok := strings.Cut(strings.TrimSpace(n), "s ")
		t, err := strconv.ParseFloat(at, 64)
		if !ok || err != nil {
			out = append(out, Event{Text: strings.TrimSpace(n)})
			continue
		}
		out = append(out, Event{T: t, Text: text, Fault: strings.HasPrefix(text, "fault ") || strings.HasPrefix(text, "kill ")})
	}
	return out
}

func keyNumbers(sum map[string]float64) []KV {
	var out []KV
	add := func(label, key, unit string, scale float64) {
		if v, ok := sum[key]; ok {
			if unit != "" && unit != "%" {
				unit = " " + unit
			}
			out = append(out, KV{label, num(v*scale) + unit})
		}
	}
	add("Availability", "availability", "%", 100)
	add("Sagas succeeded", "sagas_ok", "", 1)
	add("Sagas failed", "sagas_failed", "", 1)
	add("Throughput, mean", "sagas_per_sec.mean", "sagas/s", 1)
	add("Throughput, peak", "sagas_per_sec.max", "sagas/s", 1)
	add("Latency p50, mean", "latency_p50_ms.mean", "ms", 1)
	add("Latency p99, mean", "latency_p99_ms.mean", "ms", 1)
	add("Latency p99, worst", "latency_p99_ms.max", "ms", 1)
	add("Failure rate, mean", "error_rate.mean", "%", 100)
	add("Failure rate, worst", "error_rate.max", "%", 100)
	add("Consumer lag, peak", "consumer_lag.max", "msgs", 1)
	add("CPU, mean", "cpu_pct.mean", "% of a core", 1)
	add("CPU, peak", "cpu_pct.max", "% of a core", 1)
	if v, ok := sum["recovered"]; ok {
		if v == 1 {
			add("Recovery time", "recovery_time_s", "s", 1)
		} else {
			out = append(out, KV{"Recovery time", "did not recover within the run"})
		}
	}
	return out
}

func phaseTable(run tests.Run) ([]string, [][]string) {
	if len(run.Phases) == 0 {
		return nil, nil
	}
	cols := []string{"metric"}
	for _, p := range run.Phases {
		cols = append(cols, fmt.Sprintf("%s (%s-%ss)", p.Name, num(p.Start), num(p.End)))
	}
	var out [][]string
	for _, h := range headline {
		row := []string{h.label + ", " + h.unit}
		any := false
		for _, p := range run.Phases {
			v, ok := run.Summary[h.series+".mean@"+p.Name]
			if !ok {
				row = append(row, "-")
				continue
			}
			any = true
			if h.series == "error_rate" {
				v *= 100
			}
			row = append(row, num(v))
		}
		if any {
			out = append(out, row)
		}
	}
	return cols, out
}

// charts collects every series the run recorded into charts, the headline
// ones first, then the per-node and per-link ones together.
func charts(run tests.Run) []Chart {
	var out []Chart
	one := func(title, unit string, scale float64, names ...string) {
		c := Chart{Title: title, Unit: unit}
		for i := 0; i+1 < len(names); i += 2 {
			if pts := run.Series[names[i]]; len(pts) > 0 {
				c.Series = append(c.Series, Series{names[i+1], scaled(pts, scale)})
			}
		}
		if len(c.Series) > 0 {
			out = append(out, c)
		}
	}
	one("Throughput", "sagas/s", 1, "sagas_per_sec", "sagas finished per second")
	one("Latency", "ms", 1, "latency_p50_ms", "p50", "latency_p99_ms", "p99")
	one("Failure rate", "%", 100, "error_rate", "failed sagas")
	one("Sagas so far", "sagas", 1, "sagas_ok_total", "succeeded", "sagas_failed_total", "failed")
	if peak(run.Series["consumer_lag"]) > 0 {
		one("Consumer lag", "msgs", 1, "consumer_lag", "messages waiting")
	}
	one("Nodes up", "nodes", 1, "nodes_healthy", "healthy or degraded")
	one("CPU, all containers", "% of a core", 1, "cpu_pct", "sum")
	family := func(title, unit, prefix string, scale float64) {
		c := Chart{Title: title, Unit: unit}
		for _, name := range sortedKeys(run.Series) {
			if id, ok := member(name, prefix); ok && len(run.Series[name]) > 0 {
				c.Series = append(c.Series, Series{id, scaled(run.Series[name], scale)})
			}
		}
		if len(c.Series) > 0 {
			out = append(out, c)
		}
	}
	family("CPU per node", "% of a core", "container.cpu", 1)
	family("Memory per node", "MB", "container.mem_mb", 1)
	family("Failure rate per link", "%", "link.error_rate", 100)
	family("Requests per link", "req/s", "link.rps", 1)
	return out
}

// member splits "container.cpu[payment]" into its id, for a given prefix.
func member(name, prefix string) (string, bool) {
	if !strings.HasPrefix(name, prefix+"[") || !strings.HasSuffix(name, "]") {
		return "", false
	}
	return name[len(prefix)+1 : len(name)-1], true
}

func scaled(pts []tests.Point, k float64) []tests.Point {
	if k == 1 {
		return pts
	}
	out := make([]tests.Point, len(pts))
	for i, p := range pts {
		out[i] = tests.Point{T: p.T, V: p.V * k}
	}
	return out
}

func peak(pts []tests.Point) float64 {
	m := 0.0
	for _, p := range pts {
		m = math.Max(m, p.V)
	}
	return m
}

func rows(run tests.Run) ([]NodeRow, []EdgeRow) {
	var nodes []NodeRow
	for _, n := range run.Topology.Nodes {
		row := NodeRow{ID: n.ID, Kind: string(n.Kind), Image: n.Image, Replicas: n.Replicas, Limits: limits(n.Resources),
			Hot: config(n.Config.Hot), Cold: config(n.Config.Cold)}
		if cpu, ok := run.Series["container.cpu["+n.ID+"]"]; ok {
			row.Measured = true
			row.PeakCPU = peak(cpu)
			row.PeakMem = peak(run.Series["container.mem_mb["+n.ID+"]"])
		}
		nodes = append(nodes, row)
	}
	var edges []EdgeRow
	for _, e := range run.Topology.Edges {
		row := EdgeRow{ID: e.ID, From: e.From, To: e.To, Kind: string(e.Kind), Topic: e.Topic, TimeoutMs: e.TimeoutMs, Retries: e.Retries}
		if er, ok := run.Series["link.error_rate["+e.ID+"]"]; ok {
			row.Measured = true
			row.PeakError = peak(er)
		}
		if rps, ok := run.Series["link.rps["+e.ID+"]"]; ok {
			row.RateMeasured = true
			row.PeakRPS = peak(rps)
		}
		edges = append(edges, row)
	}
	return nodes, edges
}

func limits(r spec.Resources) string {
	cpu, mem := "no cpu limit", "no memory limit"
	if r.CPUs > 0 {
		cpu = num(r.CPUs) + " cpu"
	}
	if r.MemoryMB > 0 {
		mem = fmt.Sprintf("%d MB", r.MemoryMB)
	}
	return cpu + ", " + mem
}

func config(m map[string]any) string {
	parts := make([]string, 0, len(m))
	for _, k := range sortedKeys(m) {
		parts = append(parts, fmt.Sprintf("%s=%v", k, m[k]))
	}
	return strings.Join(parts, " ")
}

// dataRowsMax bounds the data table: the headline series are averaged over
// steps long enough for the run to fit.
const dataRowsMax = 60

func dataTable(run tests.Run, duration float64) ([]string, [][]string) {
	var have []int
	for i, h := range headline {
		if len(run.Series[h.series]) > 0 {
			have = append(have, i)
		}
	}
	if len(have) == 0 || duration <= 0 {
		return nil, nil
	}
	step := math.Max(1, math.Ceil(duration/dataRowsMax))
	cols := []string{"t, s"}
	for _, i := range have {
		cols = append(cols, headline[i].label+", "+headline[i].unit)
	}
	var out [][]string
	for from := 0.0; from < duration; from += step {
		row := []string{num(from)}
		any := false
		for _, i := range have {
			sum, n := 0.0, 0
			for _, p := range run.Series[headline[i].series] {
				if p.T >= from && p.T < from+step {
					sum += p.V
					n++
				}
			}
			if n == 0 {
				row = append(row, "-")
				continue
			}
			any = true
			v := sum / float64(n)
			if headline[i].series == "error_rate" {
				v *= 100
			}
			row = append(row, num(v))
		}
		if any {
			out = append(out, row)
		}
	}
	return cols, out
}

// num prints a number the way a table wants it: whole when it is whole,
// otherwise with as many decimals as its size calls for.
func num(v float64) string {
	a := math.Abs(v)
	switch {
	case v == math.Trunc(v) && a < 1e15:
		return strconv.FormatFloat(v, 'f', 0, 64)
	case a >= 100:
		return strconv.FormatFloat(v, 'f', 0, 64)
	case a >= 10:
		return strings.TrimSuffix(strconv.FormatFloat(v, 'f', 1, 64), ".0")
	}
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(v, 'f', 2, 64), "0"), ".")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// FileName is the name a report is saved under unless the user says
// otherwise: the run's id with the format's extension.
func FileName(runID, format string) string {
	return runID + "." + format
}

// Render writes a run's report in the given format.
func Render(run tests.Run, format string) ([]byte, error) {
	r := Build(run)
	switch format {
	case FormatMarkdown:
		return r.Markdown(), nil
	case FormatPDF:
		return r.PDF()
	}
	return nil, fmt.Errorf("report: unknown format %q (md or pdf)", format)
}
