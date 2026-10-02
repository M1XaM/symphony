package tests

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"symphony/blocks/common/control"
	"symphony/engine/core"
	"symphony/engine/events"
	"symphony/engine/spec"
	"symphony/engine/store"
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
	// Running: the run is still going; what is here is what it has so far.
	// Never stored.
	Running bool `json:"running,omitempty"`
}

// complete gives a run every list and map it may lack: one that is still
// going has no assertions yet and perhaps no phase or note, and a reader
// that walks them (the window does) must find them empty, not missing.
func (run Run) complete() Run {
	if run.Phases == nil {
		run.Phases = []PhaseSpan{}
	}
	if run.Series == nil {
		run.Series = map[string][]Point{}
	}
	if run.Summary == nil {
		run.Summary = map[string]float64{}
	}
	if run.Assertions == nil {
		run.Assertions = []AssertionResult{}
	}
	if run.Notes == nil {
		run.Notes = []string{}
	}
	if run.Params == nil {
		run.Params = map[string]any{}
	}
	return run
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
	store    *store.Store
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
	// the last state of each node, from the status events
	nodes map[string]string
}

// NewRunner stores runs in st, next to the deployments; with a nil store
// runs are evaluated but not kept.
func NewRunner(eng *core.Engine, bus *events.Bus, st *store.Store) *Runner {
	r := &Runner{eng: eng, bus: bus, store: st, active: map[string]*activeRun{}}
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
		StartedAt: time.Now().UnixMilli(), Series: map[string][]Point{}, Summary: map[string]float64{}}
	rctx, cancel := context.WithCancel(context.Background())
	a := &activeRun{run: run, cancel: cancel, live: map[string]float64{}, prevOK: map[string]float64{}, nodes: map[string]string{}}
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
		// start from the template and nothing else: no faults or load left
		// over, the config and limits it describes, and none of the state
		// (counters, idempotency records, messages, rows) of earlier runs
		progress("reset")
		if err := r.restore(ctx, run, true); err != nil {
			run.Error = "reset: " + err.Error()
			return
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

// restoreTimeout bounds putting the template back after a run; a run that
// changed replicas has containers to recreate.
const restoreTimeout = 2 * time.Minute

// restore stops the load, clears every fault and returns the deployment to
// the run's template: the topology is re-applied (which undoes replica,
// limit and hot-config changes the engine knows of) and the blocks are
// returned to it (which undoes the ones made through raw control
// commands). With state set the blocks' accumulated state is dropped too.
func (r *Runner) restore(ctx context.Context, run *Run, state bool) error {
	if gw := nodeOfKind(run.Topology, spec.KindInfra); gw != "" {
		_, _ = r.eng.Control(ctx, run.Template, gw, control.LoadStop())
	}
	for _, n := range run.Topology.Nodes {
		_ = r.eng.ClearFaults(ctx, run.Template, n.ID)
	}
	if err := r.eng.Apply(ctx, run.Topology); err != nil {
		return err
	}
	return r.eng.Baseline(ctx, run.Template, state)
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

// finish stops load and faults, evaluates the run and stores it.
func (r *Runner) finish(a *activeRun, s Scenario) {
	run := a.run
	if a.unsub != nil {
		a.unsub()
		a.unsub = nil
	}
	// leave the deployment as the template describes it, whatever the run
	// (or an abort halfway through it) changed: replicas, limits, config
	ctx, cancel := context.WithTimeout(context.Background(), restoreTimeout)
	defer cancel()
	if r.applied(run.Template) {
		if err := r.restore(ctx, run, false); err != nil {
			r.note(a, "could not restore the template: %v", err)
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
