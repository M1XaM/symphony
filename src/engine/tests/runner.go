package tests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"symphony/blocks/common/control"
	"symphony/engine/core"
	"symphony/engine/events"
	"symphony/engine/spec"
)

// Point is one sample of a series: seconds since the run started, value.
type Point struct {
	T float64 `json:"t"`
	V float64 `json:"v"`
}

// PhaseSpan is a named window of the run.
type PhaseSpan struct {
	Name  string  `json:"name"`
	Start float64 `json:"start_s"`
	End   float64 `json:"end_s"`
}

// AssertionResult is one evaluated assertion.
type AssertionResult struct {
	Assertion
	Actual float64 `json:"actual"`
	Passed bool    `json:"passed"`
}

// Run is a stored result.
type Run struct {
	ID         string             `json:"id"`
	Scenario   string             `json:"scenario"`
	Name       string             `json:"name"`
	Template   string             `json:"template"`
	Topology   spec.Topology      `json:"topology"`
	Params     map[string]any     `json:"params"`
	Runtime    string             `json:"runtime"`
	StartedAt  int64              `json:"started_at"`
	EndedAt    int64              `json:"ended_at"`
	Passed     bool               `json:"passed"`
	Aborted    bool               `json:"aborted"`
	Error      string             `json:"error,omitempty"`
	Phases     []PhaseSpan        `json:"phases"`
	Series     map[string][]Point `json:"series"`
	Summary    map[string]float64 `json:"summary"`
	Assertions []AssertionResult  `json:"assertions"`
	Notes      []string           `json:"notes"`
}

// RunSummary is what the list shows.
type RunSummary struct {
	ID        string             `json:"id"`
	Scenario  string             `json:"scenario"`
	Name      string             `json:"name"`
	Template  string             `json:"template"`
	StartedAt int64              `json:"started_at"`
	EndedAt   int64              `json:"ended_at"`
	Passed    bool               `json:"passed"`
	Aborted   bool               `json:"aborted"`
	Error     string             `json:"error,omitempty"`
	Summary   map[string]float64 `json:"summary"`
	Running   bool               `json:"running"`
}

// Runner executes scenarios.
type Runner struct {
	eng      *core.Engine
	bus      *events.Bus
	dir      string
	rtKind   string
	catalog  []Scenario
	catalogE error

	mu     sync.Mutex
	active map[string]*activeRun
}

type activeRun struct {
	run    *Run
	cancel context.CancelFunc
	start  time.Time
	mu     sync.Mutex
	phase  string
	unsub  func()
	prevOK map[string]float64
	live   map[string]float64
}

// NewRunner stores runs under dir/runs.
func NewRunner(eng *core.Engine, bus *events.Bus, dir, rtKind string) *Runner {
	r := &Runner{eng: eng, bus: bus, dir: dir, rtKind: rtKind, active: map[string]*activeRun{}}
	r.catalog, r.catalogE = loadScenarios()
	return r
}

// Scenarios lists the catalogue.
func (r *Runner) Scenarios() []Scenario {
	if r.catalog == nil {
		return []Scenario{}
	}
	return r.catalog
}

func (r *Runner) scenario(id string) (Scenario, bool) {
	for _, s := range r.catalog {
		if s.ID == id {
			return s, true
		}
	}
	return Scenario{}, false
}

// Check reports why a scenario cannot run on a template ("" = it can).
func Check(s Scenario, t spec.Topology) string {
	have := map[spec.NodeKind]bool{}
	for _, n := range t.Nodes {
		have[n.Kind] = true
	}
	for _, k := range s.Requires.Kinds {
		if !have[spec.NodeKind(k)] {
			return "template has no " + k + " node"
		}
	}
	if s.Requires.NeedsProxy && !have[spec.KindProxy] {
		return "template has no proxy"
	}
	if s.Requires.NeedsBroker && !have[spec.KindBroker] {
		return "template has no broker"
	}
	if !have[spec.KindInfra] {
		return "template has no gateway to generate load"
	}
	return ""
}

// Start launches a run and returns its id.
func (r *Runner) Start(ctx context.Context, scenarioID string, template spec.Topology, params map[string]any) (string, error) {
	if r.catalogE != nil {
		return "", r.catalogE
	}
	s, ok := r.scenario(scenarioID)
	if !ok {
		return "", fmt.Errorf("tests: unknown scenario %q", scenarioID)
	}
	template = template.Normalized()
	if why := Check(s, template); why != "" {
		return "", errors.New("tests: " + why)
	}
	r.mu.Lock()
	for _, a := range r.active {
		if a.run.Template == template.Name {
			r.mu.Unlock()
			return "", fmt.Errorf("tests: a run is already in progress on %q", template.Name)
		}
	}
	r.mu.Unlock()

	merged := map[string]any{}
	for k, v := range s.Params {
		merged[k] = v
	}
	for k, v := range params {
		merged[k] = v
	}
	id := fmt.Sprintf("%s-%s-%d", s.ID, spec.Slug(template.Name), time.Now().Unix())
	run := &Run{ID: id, Scenario: s.ID, Name: s.Name, Template: template.Name, Topology: template, Params: merged,
		Runtime: r.rtKind, StartedAt: time.Now().UnixMilli(), Series: map[string][]Point{}, Summary: map[string]float64{}}
	rctx, cancel := context.WithCancel(context.Background())
	a := &activeRun{run: run, cancel: cancel, live: map[string]float64{}, prevOK: map[string]float64{}}
	r.mu.Lock()
	r.active[id] = a
	r.mu.Unlock()
	go r.execute(rctx, s, a)
	return id, nil
}

// Abort cancels a run.
func (r *Runner) Abort(runID string) error {
	r.mu.Lock()
	a := r.active[runID]
	r.mu.Unlock()
	if a == nil {
		return fmt.Errorf("tests: no active run %q", runID)
	}
	a.cancel()
	return nil
}

func (r *Runner) note(a *activeRun, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	a.mu.Lock()
	a.run.Notes = append(a.run.Notes, fmt.Sprintf("%6.1fs %s", time.Since(a.start).Seconds(), msg))
	a.mu.Unlock()
	r.bus.Log("info", "test", "["+a.run.Template+"] "+msg)
}

// execute drives one run end to end.
func (r *Runner) execute(ctx context.Context, s Scenario, a *activeRun) {
	run := a.run
	a.start = time.Now()
	total := s.duration(run.Params)
	defer func() {
		r.finish(a, s)
	}()
	progress := func(step string) {
		a.mu.Lock()
		live := map[string]float64{}
		for k, v := range a.live {
			live[k] = v
		}
		phase := a.phase
		a.mu.Unlock()
		r.bus.Publish(events.TestProgress, events.TestProgressPayload{RunID: run.ID, Scenario: s.ID, Template: run.Template,
			Phase: phase, ElapsedMs: time.Since(a.start).Milliseconds(), TotalMs: int64(total * 1000), Step: step, Live: live})
	}

	// 1. the template must be running
	if !r.applied(run.Template) {
		r.note(a, "applying %q", run.Template)
		progress("apply")
		if err := r.eng.Apply(ctx, run.Topology); err != nil {
			run.Error = "apply: " + err.Error()
			return
		}
	} else {
		// start from a clean slate: clear faults and load left over
		for _, n := range run.Topology.Nodes {
			_ = r.eng.ClearFaults(ctx, run.Template, n.ID)
		}
		if gw := nodeOfKind(run.Topology, spec.KindInfra); gw != "" {
			_, _ = r.eng.Control(ctx, run.Template, gw, control.Command{Op: "load.stop"})
		}
	}
	a.start = time.Now()
	r.setPhase(a, "warmup")

	// 2. collect
	a.unsub = r.bus.Subscribe(func(ev events.Event) { r.collect(a, s, ev) })
	defer func() {
		if a.unsub != nil {
			a.unsub()
		}
	}()

	// 3. timeline
	steps := append([]Step(nil), s.Timeline...)
	sort.SliceStable(steps, func(i, j int) bool {
		ti, _ := toFloat(resolve(steps[i].AtS, run.Params))
		tj, _ := toFloat(resolve(steps[j].AtS, run.Params))
		return ti < tj
	})
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for _, st := range steps {
		at, _ := toFloat(resolve(st.AtS, run.Params))
		for time.Since(a.start).Seconds() < at {
			select {
			case <-ctx.Done():
				run.Aborted = true
				r.note(a, "aborted")
				return
			case <-ticker.C:
				progress("")
			}
		}
		if st.Phase != "" {
			r.setPhase(a, st.Phase)
		}
		if err := r.step(ctx, a, st); err != nil {
			if ctx.Err() != nil {
				run.Aborted = true
				r.note(a, "aborted")
				return
			}
			run.Error = fmt.Sprintf("step %s at %gs: %v", st.Do, at, err)
			r.note(a, "%s", run.Error)
			return
		}
		progress(st.Do)
	}
}

func (r *Runner) applied(name string) bool {
	for _, d := range r.eng.Deployments() {
		if d.Name == name {
			return d.Phase == core.PhaseRunning || d.Phase == core.PhaseFailed
		}
	}
	return false
}

func (r *Runner) setPhase(a *activeRun, name string) {
	now := time.Since(a.start).Seconds()
	a.mu.Lock()
	if n := len(a.run.Phases); n > 0 && a.run.Phases[n-1].End == 0 {
		a.run.Phases[n-1].End = now
	}
	a.run.Phases = append(a.run.Phases, PhaseSpan{Name: name, Start: now})
	a.phase = name
	a.mu.Unlock()
	r.note(a, "phase %s", name)
}

func nodeOfKind(t spec.Topology, k spec.NodeKind) string {
	for _, n := range t.Nodes {
		if n.Kind == k {
			return n.ID
		}
	}
	return ""
}

// target resolves a step's node / edge.
func (r *Runner) target(t spec.Topology, st Step) (node string, edge string, err error) {
	switch {
	case st.Edge == "proxied":
		for _, e := range t.Edges {
			if n, ok := t.NodeByID(e.From); ok && n.Kind == spec.KindProxy {
				return e.From, e.ID, nil
			}
		}
		for _, e := range t.Edges {
			if e.Kind == spec.EdgeCalls {
				if n, ok := t.NodeByID(e.From); ok && n.Kind != spec.KindInfra {
					return e.From, e.ID, nil
				}
			}
		}
		for _, e := range t.Edges {
			if e.Kind == spec.EdgeSubscribes {
				return e.From, e.ID, nil
			}
		}
		return "", "", errors.New("no edge to target")
	case st.Edge != "":
		for _, e := range t.Edges {
			if e.ID == st.Edge {
				return e.From, e.ID, nil
			}
		}
		return "", "", fmt.Errorf("unknown edge %q", st.Edge)
	case st.Node != "":
		if _, ok := t.NodeByID(st.Node); !ok {
			return "", "", fmt.Errorf("unknown node %q", st.Node)
		}
		return st.Node, "", nil
	case st.Kind != "":
		switch st.Kind {
		case "central":
			// the thing everything depends on: the broker, else the first service
			if n := nodeOfKind(t, spec.KindBroker); n != "" {
				return n, "", nil
			}
			return nodeOfKind(t, spec.KindService), "", nil
		case "participant":
			// a service that is called by another service (not the entry point)
			for _, e := range t.Edges {
				if from, ok := t.NodeByID(e.From); ok && from.Kind != spec.KindInfra {
					if to, ok := t.NodeByID(e.To); ok && to.Kind == spec.KindService {
						return to.ID, "", nil
					}
				}
			}
			return nodeOfKind(t, spec.KindService), "", nil
		case "consumer":
			for _, e := range t.Edges {
				if e.Kind == spec.EdgeSubscribes {
					if to, ok := t.NodeByID(e.To); ok && to.Kind == spec.KindService {
						return to.ID, "", nil
					}
				}
			}
			return nodeOfKind(t, spec.KindService), "", nil
		}
		if n := nodeOfKind(t, spec.NodeKind(st.Kind)); n != "" {
			return n, "", nil
		}
		return "", "", fmt.Errorf("no node of kind %q", st.Kind)
	}
	return "", "", nil
}

// step executes one timeline entry.
func (r *Runner) step(ctx context.Context, a *activeRun, st Step) error {
	run := a.run
	args, _ := resolve(st.Args, run.Params).(map[string]any)
	if args == nil {
		args = map[string]any{}
	}
	node, edge, err := r.target(run.Topology, st)
	if err != nil && st.Do != "phase" && st.Do != "end" && st.Do != "teardown" && st.Do != "apply" && st.Do != "load.set" && st.Do != "load.stop" {
		return err
	}
	gw := nodeOfKind(run.Topology, spec.KindInfra)
	switch st.Do {
	case "phase", "end", "wait":
		return nil
	case "load.set":
		r.note(a, "load.set %v", args)
		_, err := r.eng.Control(ctx, run.Template, gw, control.Command{Op: "load.set", Params: args})
		return err
	case "load.stop":
		r.note(a, "load.stop")
		_, err := r.eng.Control(ctx, run.Template, gw, control.Command{Op: "load.stop"})
		return err
	case "fault.set":
		kind, _ := args["kind"].(string)
		params := map[string]any{}
		dur := 0
		for k, v := range args {
			switch k {
			case "kind":
			case "duration_ms":
				if f, ok := toFloat(v); ok {
					dur = int(f)
				}
			case "duration_s":
				if f, ok := toFloat(v); ok {
					dur = int(f * 1000)
				}
			default:
				params[k] = v
			}
		}
		if edge != "" {
			params["edge"] = edge
			params["direction"] = "out"
		}
		r.note(a, "fault %s on %s %v for %dms", kind, node, params, dur)
		return r.eng.InjectFault(ctx, run.Template, node, spec.FaultSpec{Kind: spec.FaultKind(kind), DurationMs: dur, Params: params})
	case "fault.clear":
		r.note(a, "clear faults on %s", node)
		return r.eng.ClearFaults(ctx, run.Template, node)
	case "control":
		op, _ := args["op"].(string)
		params, _ := args["params"].(map[string]any)
		r.note(a, "control %s %s %v", node, op, params)
		_, err := r.eng.Control(ctx, run.Template, node, control.Command{Op: op, Params: params})
		return err
	case "config.set":
		r.note(a, "config.set %s %v", node, args)
		_, err := r.eng.Control(ctx, run.Template, node, control.Command{Op: "config.set", Params: args})
		return err
	case "resources.set":
		res := spec.Resources{}
		if f, ok := toFloat(args["cpus"]); ok {
			res.CPUs = f
		}
		if f, ok := toFloat(args["memory_mb"]); ok {
			res.MemoryMB = int(f)
		}
		if f, ok := toFloat(args["pids_max"]); ok {
			res.PidsMax = int(f)
		}
		if res == (spec.Resources{}) {
			if n, ok := run.Topology.NodeByID(node); ok {
				res = n.Resources // restore the template's limits
			}
		}
		r.note(a, "resources %s cpus=%g memory_mb=%d", node, res.CPUs, res.MemoryMB)
		return r.eng.SetResources(ctx, run.Template, node, res)
	case "restart":
		delay := 0.0
		if f, ok := toFloat(args["after_s"]); ok {
			delay = f
		}
		r.note(a, "kill %s, restart after %gs", node, delay)
		return r.eng.InjectFault(ctx, run.Template, node, spec.FaultSpec{Kind: spec.FaultKill, DurationMs: int(math.Max(delay, 0.5) * 1000), Params: map[string]any{}})
	case "replicas.set":
		n, _ := toFloat(args["replicas"])
		t := run.Topology.Clone()
		for i := range t.Nodes {
			if t.Nodes[i].ID == node {
				t.Nodes[i].Replicas = int(math.Max(1, n))
			}
		}
		r.note(a, "replicas %s -> %d (re-apply)", node, int(n))
		return r.eng.Apply(ctx, t)
	case "apply":
		r.note(a, "re-apply template")
		return r.eng.Apply(ctx, run.Topology)
	case "teardown":
		r.note(a, "teardown")
		return r.eng.Teardown(ctx, run.Template)
	}
	return fmt.Errorf("unknown step op %q", st.Do)
}

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
		add("latency_p50_ms", p.LatencyMs)
		add("latency_p99_ms", p.P99Ms)
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
		healthy := 0.0
		for _, d := range r.eng.Deployments() {
			if d.Name != a.run.Template {
				continue
			}
			for _, st := range d.Statuses {
				if st.State == "healthy" || st.State == "degraded" {
					healthy++
				}
			}
		}
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

// finish stops load and faults, evaluates the run and stores it.
func (r *Runner) finish(a *activeRun, s Scenario) {
	run := a.run
	if a.unsub != nil {
		a.unsub()
		a.unsub = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if gw := nodeOfKind(run.Topology, spec.KindInfra); gw != "" && r.applied(run.Template) {
		_, _ = r.eng.Control(ctx, run.Template, gw, control.Command{Op: "load.stop"})
		for _, n := range run.Topology.Nodes {
			_ = r.eng.ClearFaults(ctx, run.Template, n.ID)
		}
	}
	a.mu.Lock()
	if n := len(run.Phases); n > 0 && run.Phases[n-1].End == 0 {
		run.Phases[n-1].End = time.Since(a.start).Seconds()
	}
	run.EndedAt = time.Now().UnixMilli()
	r.evaluate(run, s)
	a.mu.Unlock()
	if err := r.save(run); err != nil {
		r.bus.Log("warn", "test", "could not store run: "+err.Error())
	}
	r.mu.Lock()
	delete(r.active, run.ID)
	r.mu.Unlock()
	verdict := "PASSED"
	if !run.Passed {
		verdict = "FAILED"
	}
	if run.Aborted {
		verdict = "ABORTED"
	}
	r.bus.Log("info", "test", fmt.Sprintf("[%s] %s %s (%s)", run.Template, s.Name, verdict, run.ID))
	r.bus.Publish(events.TestDone, events.TestDonePayload{RunID: run.ID, Scenario: s.ID, Template: run.Template, Passed: run.Passed, Aborted: run.Aborted, Error: run.Error})
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
		for _, p := range pts {
			if p.T >= lo && p.T <= hi {
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
	// recovery: seconds from the end of the "fault" phase until the error
	// rate stays under 2% for three samples
	for _, p := range phases {
		if p.Name != "fault" && p.Name != "outage" && p.Name != "pressure" {
			continue
		}
		pts := run.Series["error_rate"]
		streak := 0
		for _, pt := range pts {
			if pt.T < p.End {
				continue
			}
			if pt.V <= 0.02 {
				streak++
				if streak >= 3 {
					run.Summary["recovery_time_s"] = round2(pt.T - p.End)
					break
				}
			} else {
				streak = 0
			}
		}
		if _, ok := run.Summary["recovery_time_s"]; !ok && len(pts) > 0 {
			run.Summary["recovery_time_s"] = round2(pts[len(pts)-1].T - p.End)
			run.Summary["recovered"] = 0
		} else {
			run.Summary["recovered"] = 1
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

func round2(x float64) float64 { return math.Round(x*100) / 100 }

// --- storage -----------------------------------------------------------------

func (r *Runner) runsDir() string { return filepath.Join(r.dir, "runs") }

func (r *Runner) save(run *Run) error {
	if r.dir == "" {
		return errors.New("no config directory")
	}
	if err := os.MkdirAll(r.runsDir(), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(run)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(r.runsDir(), run.ID+".json"), b, 0o644)
}

// ListRuns returns stored and active runs, newest first.
func (r *Runner) ListRuns() ([]RunSummary, error) {
	var out []RunSummary
	r.mu.Lock()
	for _, a := range r.active {
		a.mu.Lock()
		out = append(out, RunSummary{ID: a.run.ID, Scenario: a.run.Scenario, Name: a.run.Name, Template: a.run.Template, StartedAt: a.run.StartedAt, Running: true})
		a.mu.Unlock()
	}
	r.mu.Unlock()
	if r.dir != "" {
		entries, err := os.ReadDir(r.runsDir())
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(r.runsDir(), e.Name()))
			if err != nil {
				continue
			}
			var run Run
			if json.Unmarshal(b, &run) != nil {
				continue
			}
			out = append(out, RunSummary{ID: run.ID, Scenario: run.Scenario, Name: run.Name, Template: run.Template, StartedAt: run.StartedAt,
				EndedAt: run.EndedAt, Passed: run.Passed, Aborted: run.Aborted, Error: run.Error, Summary: run.Summary})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	return out, nil
}

// GetRun loads one run (active runs are returned as they stand).
func (r *Runner) GetRun(id string) (Run, error) {
	r.mu.Lock()
	a := r.active[id]
	r.mu.Unlock()
	if a != nil {
		a.mu.Lock()
		defer a.mu.Unlock()
		b, _ := json.Marshal(a.run)
		var copy Run
		_ = json.Unmarshal(b, &copy)
		return copy, nil
	}
	if r.dir == "" {
		return Run{}, errors.New("no config directory")
	}
	b, err := os.ReadFile(filepath.Join(r.runsDir(), filepath.Base(id)+".json"))
	if err != nil {
		return Run{}, err
	}
	var run Run
	return run, json.Unmarshal(b, &run)
}

// DeleteRun removes a stored run.
func (r *Runner) DeleteRun(id string) error {
	if r.dir == "" {
		return errors.New("no config directory")
	}
	return os.Remove(filepath.Join(r.runsDir(), filepath.Base(id)+".json"))
}
