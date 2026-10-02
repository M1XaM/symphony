package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const runsSchema = `
CREATE TABLE IF NOT EXISTS runs (
	id         TEXT PRIMARY KEY,
	scenario   TEXT NOT NULL,
	name       TEXT NOT NULL,
	template   TEXT NOT NULL,
	started_at INTEGER NOT NULL, -- unix ms
	ended_at   INTEGER NOT NULL,
	passed     INTEGER NOT NULL,
	aborted    INTEGER NOT NULL,
	error      TEXT NOT NULL,
	summary    TEXT NOT NULL,    -- {metric: value} as JSON
	body       TEXT NOT NULL     -- the whole run (series, phases, assertions) as JSON
);
CREATE INDEX IF NOT EXISTS runs_started ON runs (started_at);`

// Run is a stored test run. The columns are what a list of runs shows; Body
// is the run as the test runner wrote it, which the store does not look
// into.
type Run struct {
	ID        string
	Scenario  string
	Name      string
	Template  string
	StartedAt int64 // unix ms
	EndedAt   int64
	Passed    bool
	Aborted   bool
	Error     string
	Summary   map[string]float64
	Body      []byte
}

// SaveRun stores a run, replacing one with the same id.
func (s *Store) SaveRun(r Run) error {
	if s == nil {
		return errors.New("store: no state database, the run is not kept")
	}
	summary, err := json.Marshal(r.Summary)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO runs (id, scenario, name, template, started_at, ended_at, passed, aborted, error, summary, body)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET scenario = excluded.scenario, name = excluded.name, template = excluded.template,
			started_at = excluded.started_at, ended_at = excluded.ended_at, passed = excluded.passed,
			aborted = excluded.aborted, error = excluded.error, summary = excluded.summary, body = excluded.body`,
		r.ID, r.Scenario, r.Name, r.Template, r.StartedAt, r.EndedAt, r.Passed, r.Aborted, r.Error, string(summary), string(r.Body))
	if err != nil {
		return fmt.Errorf("store: save run %s: %w", r.ID, err)
	}
	return nil
}

// Runs lists the stored runs, newest first, without their bodies.
func (s *Store) Runs() ([]Run, error) {
	if s == nil {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT id, scenario, name, template, started_at, ended_at, passed, aborted, error, summary
		FROM runs ORDER BY started_at DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		var summary string
		if err := rows.Scan(&r.ID, &r.Scenario, &r.Name, &r.Template, &r.StartedAt, &r.EndedAt, &r.Passed, &r.Aborted, &r.Error, &summary); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(summary), &r.Summary); err != nil {
			return nil, fmt.Errorf("store: summary of run %s: %w", r.ID, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RunBody returns a stored run as it was saved.
func (s *Store) RunBody(id string) ([]byte, bool, error) {
	if s == nil {
		return nil, false, nil
	}
	var body string
	err := s.db.QueryRow(`SELECT body FROM runs WHERE id = ?`, id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: run %s: %w", id, err)
	}
	return []byte(body), true, nil
}

// DeleteRun removes a stored run; false when there was none.
func (s *Store) DeleteRun(id string) (bool, error) {
	if s == nil {
		return false, nil
	}
	res, err := s.db.Exec(`DELETE FROM runs WHERE id = ?`, id)
	if err != nil {
		return false, fmt.Errorf("store: delete run %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ImportRuns takes over the runs Symphony kept as one JSON file each (in
// <config dir>/runs) before they lived in this database. It runs once per
// database, leaves the files where they are and returns how many it
// imported.
func (s *Store) ImportRuns(dir string) (int, error) {
	if s == nil || dir == "" {
		return 0, nil
	}
	if _, done, err := s.meta("runs_imported"); err != nil || done {
		return 0, err
	}
	n := 0
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var rec struct {
			ID        string             `json:"id"`
			Scenario  string             `json:"scenario"`
			Name      string             `json:"name"`
			Template  string             `json:"template"`
			StartedAt int64              `json:"started_at"`
			EndedAt   int64              `json:"ended_at"`
			Passed    bool               `json:"passed"`
			Aborted   bool               `json:"aborted"`
			Error     string             `json:"error"`
			Summary   map[string]float64 `json:"summary"`
		}
		if json.Unmarshal(b, &rec) != nil || rec.ID == "" {
			continue
		}
		if _, exists, err := s.RunBody(rec.ID); err != nil || exists {
			continue
		}
		run := Run{ID: rec.ID, Scenario: rec.Scenario, Name: rec.Name, Template: rec.Template, StartedAt: rec.StartedAt,
			EndedAt: rec.EndedAt, Passed: rec.Passed, Aborted: rec.Aborted, Error: rec.Error, Summary: rec.Summary, Body: b}
		if err := s.SaveRun(run); err != nil {
			return n, err
		}
		n++
	}
	return n, s.setMeta("runs_imported", time.Now().UTC().Format(time.RFC3339))
}
