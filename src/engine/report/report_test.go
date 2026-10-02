package report

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"

	"symphony/engine/spec"
	"symphony/engine/tests"
)

func ramp(n int, f func(i int) float64) []tests.Point {
	out := make([]tests.Point, n)
	for i := range out {
		out[i] = tests.Point{T: float64(i), V: f(i)}
	}
	return out
}

// sample is a run as the runner stores one: a crash scenario on the
// choreography preset, with a stretch in which no saga finished.
func sample() tests.Run {
	topo := spec.Presets()[1]
	latency := append(ramp(20, func(int) float64 { return 55 }), tests.Point{T: 30, V: 9000}, tests.Point{T: 31, V: 60})
	return tests.Run{
		ID: "t04-crash-demo-1", Scenario: "t04-crash", Name: "Service crash mid-saga", Template: topo.Name, Topology: topo,
		Params:    map[string]any{"rps": 40.0, "down_s": 10.0},
		StartedAt: 1_790_000_000_000, EndedAt: 1_790_000_032_000,
		Passed: false,
		Phases: []tests.PhaseSpan{{Name: "steady", Start: 0, End: 20}, {Name: "fault", Start: 20, End: 30}, {Name: "recovery", Start: 30, End: 32}},
		Series: map[string][]tests.Point{
			"sagas_per_sec":              ramp(32, func(i int) float64 { return 40 }),
			"latency_p50_ms":             latency,
			"latency_p99_ms":             latency,
			"error_rate":                 ramp(32, func(i int) float64 { return map[bool]float64{true: 0.5}[i >= 30] }),
			"consumer_lag":               ramp(32, func(i int) float64 { return float64(i) }),
			"container.cpu[payment]":     ramp(32, func(i int) float64 { return 20 }),
			"container.mem_mb[payment]":  ramp(32, func(i int) float64 { return 9 }),
			"link.error_rate[e-pay-sub]": ramp(32, func(i int) float64 { return 1 }),
		},
		Summary: map[string]float64{"availability": 0.93, "sagas_ok": 1200, "sagas_failed": 90, "recovered": 0,
			"sagas_per_sec.mean@steady": 40, "error_rate.mean@recovery": 0.5, "latency_p99_ms.max": 9000},
		Assertions: []tests.AssertionResult{
			{Assertion: tests.Assertion{Metric: "error_rate.mean", Op: "<=", Value: 0.01, Phase: "steady"}, Actual: 0, Passed: true},
			{Assertion: tests.Assertion{Metric: "recovery_time_s", Op: "<=", Value: 30, Note: "back within 30s | of the restart"}, Passed: false},
		},
		Notes: []string{"   0.0s phase steady", "  20.0s kill payment, restart after 10s", "  30.0s phase recovery", "aborted"},
	}
}

func TestBuild(t *testing.T) {
	r := Build(sample())
	if r.Verdict != "FAILED" || r.Duration != 32 || r.Description == "" || len(r.Params) != 2 {
		t.Fatalf("verdict %q, duration %v, description %q, params %v", r.Verdict, r.Duration, r.Description, r.Params)
	}
	if len(r.Timeline) != 4 || r.Timeline[1].T != 20 || !r.Timeline[1].Fault || r.Timeline[0].Fault || r.Timeline[3].Text != "aborted" {
		t.Fatalf("timeline %+v", r.Timeline)
	}
	var titles []string
	for _, c := range r.Charts {
		titles = append(titles, c.Title)
	}
	if got := strings.Join(titles, ", "); got != "Throughput, Latency, Failure rate, Consumer lag, CPU per node, Memory per node, Failure rate per link" {
		t.Fatalf("charts: %s", got)
	}
	// the failure rate is charted in percent
	if c := r.Charts[2]; c.Unit != "%" || peak(c.Series[0].Points) != 50 {
		t.Fatalf("failure rate chart: %+v", c)
	}
	var payment NodeRow
	for _, n := range r.Nodes {
		if n.ID == "payment" {
			payment = n
		}
	}
	if !payment.Measured || payment.PeakCPU != 20 || payment.PeakMem != 9 || payment.Replicas != 2 {
		t.Fatalf("payment: %+v", payment)
	}
	for _, e := range r.Edges {
		if e.ID == "e-pay-sub" && (!e.Measured || e.RateMeasured || e.PeakError != 1) {
			t.Fatalf("e-pay-sub: %+v", e)
		}
	}
	keys := map[string]string{}
	for _, kv := range r.KeyNumbers {
		keys[kv.Key] = kv.Value
	}
	if keys["Availability"] != "93%" || keys["Recovery time"] != "did not recover within the run" {
		t.Fatalf("key numbers %v", keys)
	}
	if len(r.PhaseColumns) != 4 || r.PhaseRows[0][1] != "40" || r.PhaseRows[0][2] != "-" {
		t.Fatalf("phase table %v %v", r.PhaseColumns, r.PhaseRows)
	}
	if len(r.DataRows) != 32 || r.DataRows[25][2] != "-" {
		t.Fatalf("data table: %d rows, %v", len(r.DataRows), r.DataRows[25])
	}
}

func TestMarkdown(t *testing.T) {
	md := string(Build(sample()).Markdown())
	for _, want := range []string{
		"# Service crash mid-saga on \"Choreography (saga)\": FAILED",
		"| Verdict | FAILED (1 of 2 assertions hold) |",
		"| **FAIL** | `recovery_time_s` | <= 30 | - | 0 | back within 30s \\| of the restart |",
		"## Topology", "### Components",
		"- **`payment`**: service, 2 replicas, image `symphony/service:dev`, 0.5 cpu, 256 MB\n  - peak use: 20% of a core, 9 MB\n  - hot config: `breaker_threshold=5",
		"- **`kafka`**: broker, 1 replica,",
		"### Connections",
		"- `gateway` calls `order` (edge `e-gw-order`)\n",
		"- `order` publishes `order.created` to `kafka` (edge `e-order-pub`)\n",
		"- `toxiproxy` delivers `order.created` to `payment` (edge `e-pay-sub`): peak failure rate 100%\n",
		"- `order` uses `postgres` (edge `e-order-db`)\n",
		"## Results", "### Per phase (mean)", "## Timeline", "| 20s | kill payment, restart after 10s |",
		"## Data", "## Every summary value", "| `availability` | 0.93 |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("the Markdown lacks %q", want)
		}
	}
	// text only: the picture and the charts are the PDF's
	for _, not := range []string{"data:image", "<svg", "![", "## Charts"} {
		if strings.Contains(md, not) {
			t.Errorf("the Markdown contains %q", not)
		}
	}
	// every component and every connection is described
	topo := spec.Presets()[1]
	if got := strings.Count(md, "\n- **`"); got != len(topo.Nodes) {
		t.Errorf("%d components listed, the topology has %d", got, len(topo.Nodes))
	}
	for _, e := range topo.Edges {
		if !strings.Contains(md, "(edge `"+e.ID+"`") {
			t.Errorf("the connection %s is not described", e.ID)
		}
	}
}

// The picture of the topology shows every node.
func TestTopologyDrawsEveryNode(t *testing.T) {
	topo := spec.Presets()[1]
	_, h, _ := topologySize(topo, 500)
	img := newSVG(500, h)
	drawTopology(img, topo, 500)
	if err := xml.Unmarshal(img.Bytes(), new(struct{})); err != nil {
		t.Fatalf("not well-formed: %v", err)
	}
	for _, n := range topo.Nodes {
		if !bytes.Contains(img.Bytes(), []byte(">"+n.ID+"</text>")) {
			t.Errorf("the picture does not show %s", n.ID)
		}
	}
	if got := bytes.Count(img.Bytes(), []byte("<polygon")); got != len(topo.Edges) {
		t.Errorf("%d arrows for %d edges", got, len(topo.Edges))
	}
}

// Where a series has no samples for a while the line stops: it does not
// ramp across the gap.
func TestChartBreaksAtGaps(t *testing.T) {
	r := Build(sample())
	img := newSVG(400, chartHeight(r.Charts[1], 400))
	drawChart(img, 400, r.Charts[1], r.Phases, r.Timeline, r.Duration)
	if got := bytes.Count(img.Bytes(), []byte("<polyline")); got != 4 { // two series, two stretches each
		t.Fatalf("%d polylines, want 4", got)
	}
	// the kill is marked
	if !bytes.Contains(img.Bytes(), []byte("stroke-dasharray")) {
		t.Fatal("the fault is not marked on the chart")
	}
}

func TestPDF(t *testing.T) {
	run := sample()
	run.Template = "Наш шаблон" // outside the built-in fonts' encoding: must not break the document
	doc, err := Build(run).PDF()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(doc, []byte("%PDF-")) || !bytes.Contains(doc[len(doc)-16:], []byte("%%EOF")) || len(doc) < 8000 {
		t.Fatalf("not a PDF document (%d bytes)", len(doc))
	}
	if pages := bytes.Count(doc, []byte("/Type /Page\n")); pages < 3 {
		t.Fatalf("%d pages", pages)
	}
}

func TestRender(t *testing.T) {
	for _, f := range []string{FormatMarkdown, FormatPDF} {
		if doc, err := Render(sample(), f); err != nil || len(doc) == 0 {
			t.Errorf("%s: %d bytes, %v", f, len(doc), err)
		}
	}
	if _, err := Render(sample(), "docx"); err == nil {
		t.Error("an unknown format was rendered")
	}
	if FileName("t04-x-1", FormatPDF) != "t04-x-1.pdf" {
		t.Error("FileName")
	}
	// a run with nothing in it still reports
	if doc, err := Render(tests.Run{ID: "empty", Aborted: true}, FormatPDF); err != nil || len(doc) == 0 {
		t.Errorf("an empty run: %v", err)
	}
	if md := string(Build(tests.Run{ID: "empty", Aborted: true}).Markdown()); !strings.Contains(md, "ABORTED") {
		t.Error("an empty run's Markdown")
	}
}
