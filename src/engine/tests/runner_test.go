package tests

import (
	"testing"

	"symphony/engine/spec"
)

func series(vals ...float64) []Point {
	out := make([]Point, len(vals))
	for i, v := range vals {
		out[i] = Point{T: float64(i), V: v}
	}
	return out
}

func TestScenariosLoadAndFitThePresets(t *testing.T) {
	all, err := loadScenarios()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 18 {
		t.Fatalf("%d scenarios, want 18", len(all))
	}
	ops := map[string]bool{"phase": true, "end": true, "wait": true, "load.set": true, "load.stop": true, "fault.set": true,
		"fault.clear": true, "control": true, "config.set": true, "resources.set": true, "restart": true,
		"replicas.set": true, "apply": true, "teardown": true}
	presets := spec.Presets()
	for _, s := range all {
		for _, st := range s.Timeline {
			if !ops[st.Do] {
				t.Errorf("%s: unknown step %q", s.ID, st.Do)
			}
			if st.Do == "fault.set" {
				kind, _ := resolve(st.Args, s.Params).(map[string]any)["kind"].(string)
				if !spec.FaultKind(kind).Valid() {
					t.Errorf("%s: unknown fault kind %q", s.ID, kind)
				}
			}
		}
		if s.duration(s.Params) <= 1 {
			t.Errorf("%s: no duration", s.ID)
		}
		fits := false
		for _, p := range presets {
			fits = fits || Check(s, p) == ""
		}
		if !fits {
			t.Errorf("%s runs on neither preset", s.ID)
		}
	}
}

func TestResolveSubstitutesParameters(t *testing.T) {
	params := map[string]any{"rps": 40.0, "name": "x"}
	got := resolve(map[string]any{"rps": "{rps}", "label": "load-{name}", "keep": "{nope}", "list": []any{"{rps}"}}, params).(map[string]any)
	if got["rps"] != 40.0 || got["label"] != "load-x" || got["keep"] != "{nope}" || got["list"].([]any)[0] != 40.0 {
		t.Fatalf("resolved %v", got)
	}
}

// Assertions aggregate a series over a phase; recovery is the time from the
// end of the fault until the error rate has stayed low for three samples.
func TestEvaluate(t *testing.T) {
	run := &Run{
		Phases: []PhaseSpan{{Name: "steady", Start: 0, End: 3}, {Name: "fault", Start: 3, End: 6}, {Name: "recovery", Start: 6, End: 12}},
		Series: map[string][]Point{
			//                       steady      fault          recovery
			"error_rate":         series(0, 0, 0, 0.5, 0.6, 0.5, 0.4, 0.1, 0, 0, 0, 0),
			"latency_p99_ms":     series(20, 20, 20, 900, 900, 900, 300, 30, 20, 20, 20, 20),
			"sagas_ok_total":     series(10, 20, 30, 35, 40, 45, 50, 60, 70, 80, 90, 90),
			"sagas_failed_total": series(0, 0, 0, 5, 10, 10, 10, 10, 10, 10, 10, 10),
		},
		Summary: map[string]float64{},
	}
	s := Scenario{Assert: []Assertion{
		{Metric: "error_rate", Op: "<=", Value: 0.01, Phase: "steady"},
		{Metric: "latency_p99_ms.max", Op: "<=", Value: 500, Phase: "fault"},
		{Metric: "availability", Op: ">=", Value: 0.8},
		{Metric: "recovery_time_s", Op: "<=", Value: 5},
		{Metric: "error_rate", Op: "<", Value: 1, Phase: "no-such-phase"},
	}}
	(&Runner{}).evaluate(run, s)

	want := []bool{true, false, true, true, false}
	for i, a := range run.Assertions {
		if a.Passed != want[i] {
			t.Errorf("assertion %d (%s %s %v @%s): passed %v with actual %v", i, a.Metric, a.Op, a.Value, a.Phase, a.Passed, a.Actual)
		}
	}
	if run.Passed {
		t.Error("a run with a failed assertion passed")
	}
	// the error rate is back under 2% from t=8 on, two seconds after the
	// fault ended at t=6
	if got := run.Summary["recovery_time_s"]; got != 2 || run.Summary["recovered"] != 1 {
		t.Errorf("recovery_time_s %v, recovered %v", got, run.Summary["recovered"])
	}
	if got := run.Summary["availability"]; got != 0.9 {
		t.Errorf("availability %v, want 0.9", got)
	}
	if got := run.Summary["latency_p99_ms.mean@fault"]; got != 900 {
		t.Errorf("mean p99 in the fault phase %v, want 900", got)
	}
}

func TestRecovery(t *testing.T) {
	phases := []PhaseSpan{{Name: "steady", Start: 0, End: 2}, {Name: "outage", Start: 2, End: 4}, {Name: "recovery", Start: 4, End: 9}}
	for _, tc := range []struct {
		name      string
		errors    []Point
		rate      []Point
		secs      float64
		recovered bool
		measured  bool
	}{
		{"settles after a second", series(0, 0, 1, 1, 0.5, 0, 0, 0, 0), nil, 1, true, true},
		{"a relapse restarts the count", series(0, 0, 1, 1, 0, 0, 0.3, 0, 0, 0), nil, 3, true, true},
		{"never settles", series(0, 0, 1, 1, 0.5, 0.4, 0.3, 0.2, 0.1), nil, 0, false, true},
		{"nothing flows, though nothing fails", series(0, 0, 1, 1, 0, 0, 0, 0, 0), series(40, 40, 0, 0, 0, 0, 0, 0, 0), 0, false, true},
		{"flows again at t=6", series(0, 0, 1, 1, 0, 0, 0, 0, 0), series(40, 40, 0, 0, 0, 0, 30, 40, 40), 2, true, true},
		{"the run ended with the outage", series(0, 0, 1, 1), nil, 0, false, false},
	} {
		run := &Run{Phases: phases, Series: map[string][]Point{"error_rate": tc.errors}}
		if tc.rate != nil {
			run.Series["sagas_per_sec"] = tc.rate
		}
		secs, recovered, measured := recovery(run)
		if secs != tc.secs || recovered != tc.recovered || measured != tc.measured {
			t.Errorf("%s: %v %v %v, want %v %v %v", tc.name, secs, recovered, measured, tc.secs, tc.recovered, tc.measured)
		}
	}
	if _, _, measured := recovery(&Run{Phases: phases[:1], Series: map[string][]Point{"error_rate": series(0, 0)}}); measured {
		t.Error("a run without a disturbance phase has no recovery to measure")
	}

	// a system that stayed broken fails an assertion on its recovery time
	run := &Run{Phases: phases, Series: map[string][]Point{"error_rate": series(0, 0, 1, 1, 0.5, 0.4, 0.3, 0.2, 0.1)}, Summary: map[string]float64{}}
	(&Runner{}).evaluate(run, Scenario{Assert: []Assertion{{Metric: "recovery_time_s", Op: "<=", Value: 30}}})
	if run.Passed || run.Assertions[0].Passed || run.Summary["recovered"] != 0 {
		t.Errorf("not recovered, yet: passed %v, summary %v", run.Passed, run.Summary)
	}
}
