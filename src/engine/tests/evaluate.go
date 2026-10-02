// Collecting a run's series from the bus and judging it afterwards: the
// per-phase summary, the recovery time and the assertions.

package tests

import (
	"math"
	"strings"
	"time"

	"symphony/engine/events"
)

// collect turns bus events into series points.
func (r *Runner) collect(a *activeRun, s Scenario, ev events.Event) {
	t := time.Since(a.start).Seconds()
	add := func(name string, v float64) {
		if !wanted(s, name) {
			return
		}
		a.mu.Lock()
		a.run.Series[name] = append(a.run.Series[name], Point{T: math.Round(t*100) / 100, V: v})
		a.live[name] = v
		a.mu.Unlock()
	}
	switch p := ev.Payload.(type) {
	case events.MetricsPayload:
		if p.Deployment != a.run.Template {
			return
		}
		add("sagas_per_sec", p.SagasPerSec)
		// no saga finished in the collector's window: there is no latency
		// to record, and a zero would drag the phase's mean down
		if p.P99Ms > 0 {
			add("latency_p50_ms", p.LatencyMs)
			add("latency_p99_ms", p.P99Ms)
		}
		add("error_rate", p.ErrorRate)
		add("consumer_lag", float64(p.ConsumerLag))
		add("cpu_pct", p.CPUPct)
		a.mu.Lock()
		a.prevOK["ok"] += float64(p.SagasOK)
		a.prevOK["failed"] += float64(p.SagasFailed)
		ok, failed := a.prevOK["ok"], a.prevOK["failed"]
		a.mu.Unlock()
		add("sagas_ok_total", ok)
		add("sagas_failed_total", failed)
	case events.LinkHealthPayload:
		if p.Deployment != a.run.Template {
			return
		}
		for edge, e := range p.Links {
			add("link.error_rate["+edge+"]", e.ErrorRate)
			add("link.rps["+edge+"]", e.RPS)
		}
	case events.ContainerStatsPayload:
		if p.Deployment != a.run.Template {
			return
		}
		add("container.cpu["+p.NodeID+"]", p.CPUPct)
		add("container.mem_mb["+p.NodeID+"]", float64(p.MemUsed>>20))
	case events.NodeStatusPayload:
		if p.Deployment != a.run.Template {
			return
		}
		a.mu.Lock()
		if p.State == "removed" {
			delete(a.nodes, p.NodeID)
		} else {
			a.nodes[p.NodeID] = p.State
		}
		healthy := 0.0
		for _, state := range a.nodes {
			if state == "healthy" || state == "degraded" {
				healthy++
			}
		}
		a.mu.Unlock()
		add("nodes_healthy", healthy)
	}
}

func wanted(s Scenario, name string) bool {
	if len(s.Collect) == 0 {
		return true
	}
	for _, c := range s.Collect {
		if c == "*" || c == name {
			return true
		}
		if strings.HasSuffix(c, "*") && strings.HasPrefix(name, strings.TrimSuffix(c, "*")) {
			return true
		}
		if strings.HasSuffix(c, "[*]") && strings.HasPrefix(name, strings.TrimSuffix(c, "[*]")+"[") {
			return true
		}
	}
	return false
}

// evaluate fills Summary (per-phase aggregates of every series) and the assertions.
func (r *Runner) evaluate(run *Run, s Scenario) {
	phases := run.Phases
	agg := func(name, phase, fn string) (float64, bool) {
		pts := run.Series[name]
		var lo, hi float64 = 0, math.Inf(1)
		if phase != "" {
			found := false
			for _, p := range phases {
				if p.Name == phase {
					lo, hi, found = p.Start, p.End, true
				}
			}
			if !found {
				return 0, false
			}
		}
		var vals []float64
		// a phase holds [start, end): the sample taken as the next one
		// begins belongs to the next one only
		for _, p := range pts {
			if p.T >= lo && p.T < hi {
				vals = append(vals, p.V)
			}
		}
		if len(vals) == 0 {
			return 0, false
		}
		switch fn {
		case "max":
			m := vals[0]
			for _, v := range vals {
				m = math.Max(m, v)
			}
			return m, true
		case "min":
			m := vals[0]
			for _, v := range vals {
				m = math.Min(m, v)
			}
			return m, true
		case "last":
			return vals[len(vals)-1], true
		case "first":
			return vals[0], true
		}
		sum := 0.0
		for _, v := range vals {
			sum += v
		}
		return sum / float64(len(vals)), true
	}
	// summary: mean/max of the headline series per phase
	for _, name := range []string{"sagas_per_sec", "latency_p50_ms", "latency_p99_ms", "error_rate", "consumer_lag", "cpu_pct"} {
		if v, ok := agg(name, "", "mean"); ok {
			run.Summary[name+".mean"] = round2(v)
		}
		if v, ok := agg(name, "", "max"); ok {
			run.Summary[name+".max"] = round2(v)
		}
		for _, p := range phases {
			if v, ok := agg(name, p.Name, "mean"); ok {
				run.Summary[name+".mean@"+p.Name] = round2(v)
			}
		}
	}
	if v, ok := agg("sagas_ok_total", "", "last"); ok {
		run.Summary["sagas_ok"] = v
	}
	if v, ok := agg("sagas_failed_total", "", "last"); ok {
		run.Summary["sagas_failed"] = v
	}
	if ok, hasOK := run.Summary["sagas_ok"]; hasOK {
		failed := run.Summary["sagas_failed"]
		if ok+failed > 0 {
			run.Summary["availability"] = round2(ok / (ok + failed))
		}
	}
	if secs, recovered, measured := recovery(run); measured {
		run.Summary["recovered"] = 0
		if recovered {
			run.Summary["recovered"] = 1
			run.Summary["recovery_time_s"] = round2(secs)
		}
	}
	run.Passed = run.Error == "" && !run.Aborted
	for _, as := range s.Assert {
		name, fn := as.Metric, "mean"
		for _, suffix := range []string{".mean", ".max", ".min", ".last", ".first"} {
			if strings.HasSuffix(name, suffix) {
				fn = strings.TrimPrefix(suffix, ".")
				name = strings.TrimSuffix(name, suffix)
			}
		}
		actual, ok := agg(name, as.Phase, fn)
		if !ok {
			if v, has := run.Summary[as.Metric]; has {
				actual, ok = v, true
			}
		}
		res := AssertionResult{Assertion: as, Actual: round2(actual)}
		if ok {
			switch as.Op {
			case "<=":
				res.Passed = actual <= as.Value
			case "<":
				res.Passed = actual < as.Value
			case ">=":
				res.Passed = actual >= as.Value
			case ">":
				res.Passed = actual > as.Value
			case "==":
				res.Passed = math.Abs(actual-as.Value) < 1e-9
			}
		}
		if !res.Passed {
			run.Passed = false
		}
		run.Assertions = append(run.Assertions, res)
	}
	if run.Assertions == nil {
		run.Assertions = []AssertionResult{}
	}
}

// disturbance names the phases a run recovers from.
var disturbance = map[string]bool{"fault": true, "outage": true, "pressure": true}

// recovery measures how long the system took to be well again after the
// last disturbance phase: the time from the end of that phase to the first
// of three samples in a row in which sagas finish and at most 2% of them
// fail. A sample in which nothing finishes is not a recovered one, though
// its error rate reads zero. measured is false when the run has no such
// phase or ended with it; recovered is false when the samples after it
// never settle, and then there is no time to report: an assertion on
// recovery_time_s must not pass on a system that stayed broken.
func recovery(run *Run) (seconds float64, recovered, measured bool) {
	end := -1.0
	for _, p := range run.Phases {
		if disturbance[p.Name] && p.End > end {
			end = p.End
		}
	}
	if end < 0 {
		return 0, false, false
	}
	// throughput by sample time, when the scenario collected it
	rate := map[float64]float64{}
	for _, pt := range run.Series["sagas_per_sec"] {
		rate[pt.T] = pt.V
	}
	streak, first := 0, 0.0
	for _, pt := range run.Series["error_rate"] {
		if pt.T < end {
			continue
		}
		measured = true
		flowing := true
		if v, known := rate[pt.T]; known {
			flowing = v > 0
		}
		if pt.V > 0.02 || !flowing {
			streak = 0
			continue
		}
		if streak == 0 {
			first = pt.T
		}
		if streak++; streak >= 3 {
			return first - end, true, true
		}
	}
	return 0, false, measured
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }
