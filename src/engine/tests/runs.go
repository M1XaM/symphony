// Stored runs, kept in the state database.

package tests

import (
	"encoding/json"
	"fmt"
	"sort"

	"symphony/engine/store"
)

func (r *Runner) save(run *Run) error {
	b, err := json.Marshal(run)
	if err != nil {
		return err
	}
	return r.store.SaveRun(store.Run{ID: run.ID, Scenario: run.Scenario, Name: run.Name, Template: run.Template,
		StartedAt: run.StartedAt, EndedAt: run.EndedAt, Passed: run.Passed, Aborted: run.Aborted, Error: run.Error,
		Summary: run.Summary, Body: b})
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
	stored, err := r.store.Runs()
	if err != nil {
		return nil, err
	}
	for _, s := range stored {
		out = append(out, RunSummary{ID: s.ID, Scenario: s.Scenario, Name: s.Name, Template: s.Template, StartedAt: s.StartedAt,
			EndedAt: s.EndedAt, Passed: s.Passed, Aborted: s.Aborted, Error: s.Error, Summary: s.Summary})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
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
	b, ok, err := r.store.RunBody(id)
	if err != nil {
		return Run{}, err
	}
	if !ok {
		return Run{}, fmt.Errorf("tests: no run %q", id)
	}
	var run Run
	return run, json.Unmarshal(b, &run)
}

// DeleteRun removes a stored run.
func (r *Runner) DeleteRun(id string) error {
	ok, err := r.store.DeleteRun(id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("tests: no run %q", id)
	}
	return nil
}
