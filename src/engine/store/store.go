// Package store keeps Symphony's state in one SQLite file next to the
// binary: the instance id that labels everything it creates, and the current
// state of every deployment (the topology, its admin token and network, its
// phase and the load the gateway was told to drive). A Symphony that was
// closed reads it back on start and reopens what was running; the GUI and
// the CLI next to it share the file.
package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure Go: no cgo, so every build target keeps working

	"symphony/engine/spec"
)

// FileName is the database's name next to the binary.
const FileName = "symphony.db"

const schemaVersion = 1

const schema = `
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS deployments (
	slug       TEXT PRIMARY KEY,
	name       TEXT NOT NULL UNIQUE,
	token      TEXT NOT NULL,
	network    TEXT NOT NULL,
	phase      TEXT NOT NULL,
	topology   TEXT NOT NULL, -- spec.Topology as JSON
	load       TEXT,          -- {node id: load.set params} as JSON, NULL when idle
	rev        INTEGER NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);`

// Deployment is the stored state of one applied topology.
type Deployment struct {
	Name     string
	Slug     string
	Token    string
	Network  string
	Phase    string
	Topology spec.Topology
	// Load holds the open-ended load.set each node was last given, so a
	// reopened deployment drives the same load again.
	Load map[string]map[string]any
	// Rev goes up by one on every write, so a process can tell that another
	// one changed the deployment.
	Rev       int64
	UpdatedAt time.Time
}

// Store is safe for concurrent use, and for use by several processes at once.
// A nil *Store remembers nothing and reports no error.
type Store struct {
	db   *sql.DB
	path string
}

// DefaultPath is $SYMPHONY_DB, else symphony.db in the directory of the
// running binary.
func DefaultPath() (string, error) {
	if p := os.Getenv("SYMPHONY_DB"); p != "" {
		return p, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("store: locate the binary: %w", err)
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return filepath.Join(filepath.Dir(exe), FileName), nil
}

// Open opens (creating if needed) the database at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// One connection: the pragmas below are per connection, and the app
	// writes a handful of rows per user action.
	db.SetMaxOpenConns(1)
	// busy_timeout lets the GUI and the CLI write the same file in turn.
	for _, q := range []string{"PRAGMA busy_timeout = 5000", "PRAGMA synchronous = FULL", schema,
		fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)} {
		if _, err := db.Exec(q); err != nil {
			db.Close()
			return nil, fmt.Errorf("store: %s: %w", path, err)
		}
	}
	return &Store{db: db, path: path}, nil
}

// Path is the database file.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	return s.db.Close()
}

// --- meta ------------------------------------------------------------------

func (s *Store) meta(key string) (string, bool, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

func (s *Store) setMeta(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// InstanceID returns the owner id put on every container and network this
// installation creates, minting it on first use.
func (s *Store) InstanceID() (string, error) {
	if s == nil {
		return RandomID(6), nil
	}
	if id, ok, err := s.meta("instance"); err != nil || ok {
		return id, err
	}
	id := RandomID(6)
	return id, s.setMeta("instance", id)
}

// RandomID returns n random bytes as hex.
func RandomID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// --- deployments -----------------------------------------------------------

const columns = `name, slug, token, network, phase, topology, load, rev, updated_at`

type scanner interface{ Scan(...any) error }

func scan(row scanner) (Deployment, error) {
	var d Deployment
	var topo string
	var load sql.NullString
	var updated int64
	if err := row.Scan(&d.Name, &d.Slug, &d.Token, &d.Network, &d.Phase, &topo, &load, &d.Rev, &updated); err != nil {
		return Deployment{}, err
	}
	if err := json.Unmarshal([]byte(topo), &d.Topology); err != nil {
		return Deployment{}, fmt.Errorf("store: topology of %q: %w", d.Name, err)
	}
	d.Topology = d.Topology.Normalized()
	if load.Valid {
		if err := json.Unmarshal([]byte(load.String), &d.Load); err != nil {
			return Deployment{}, fmt.Errorf("store: load of %q: %w", d.Name, err)
		}
	}
	d.UpdatedAt = time.UnixMilli(updated)
	return d, nil
}

// Deployments returns every stored deployment, by name.
func (s *Store) Deployments() ([]Deployment, error) {
	if s == nil {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT ` + columns + ` FROM deployments ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	defer rows.Close()
	var out []Deployment
	for rows.Next() {
		d, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Deployment returns one stored deployment by slug.
func (s *Store) Deployment(slug string) (Deployment, bool, error) {
	if s == nil {
		return Deployment{}, false, nil
	}
	d, err := scan(s.db.QueryRow(`SELECT `+columns+` FROM deployments WHERE slug = ?`, slug))
	if errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, false, nil
	}
	return d, err == nil, err
}

// Save writes a deployment whole and returns its new revision.
func (s *Store) Save(d Deployment) (int64, error) {
	if s == nil {
		return 0, nil
	}
	topo, err := json.Marshal(d.Topology)
	if err != nil {
		return 0, err
	}
	load, err := loadJSON(d.Load)
	if err != nil {
		return 0, err
	}
	now := time.Now().UnixMilli()
	var rev int64
	err = s.db.QueryRow(`INSERT INTO deployments (slug, name, token, network, phase, topology, load, rev, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT (slug) DO UPDATE SET name = excluded.name, token = excluded.token, network = excluded.network,
			phase = excluded.phase, topology = excluded.topology, load = excluded.load,
			rev = deployments.rev + 1, updated_at = excluded.updated_at
		RETURNING rev`,
		d.Slug, d.Name, d.Token, d.Network, d.Phase, string(topo), load, now, now).Scan(&rev)
	if err != nil {
		return 0, fmt.Errorf("store: save %q: %w", d.Name, err)
	}
	return rev, nil
}

// SetPhase records a deployment's phase and returns its new revision; 0
// when the deployment is not stored.
func (s *Store) SetPhase(slug, phase string) (int64, error) {
	return s.update(slug, `phase = ?`, phase)
}

// SetLoad records the load its nodes drive (nil: none).
func (s *Store) SetLoad(slug string, load map[string]map[string]any) (int64, error) {
	v, err := loadJSON(load)
	if err != nil {
		return 0, err
	}
	return s.update(slug, `load = ?`, v)
}

func (s *Store) update(slug, set string, value any) (int64, error) {
	if s == nil {
		return 0, nil
	}
	var rev int64
	err := s.db.QueryRow(`UPDATE deployments SET `+set+`, rev = rev + 1, updated_at = ? WHERE slug = ? RETURNING rev`,
		value, time.Now().UnixMilli(), slug).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("store: update %s: %w", slug, err)
	}
	return rev, nil
}

// Delete forgets a deployment.
func (s *Store) Delete(slug string) error {
	if s == nil {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM deployments WHERE slug = ?`, slug)
	return err
}

func loadJSON(load map[string]map[string]any) (any, error) {
	if len(load) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(load)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// --- migration ---------------------------------------------------------------

// ImportLegacy takes over what Symphony kept in its config directory before
// it had this database: the instance id (so the containers it left running
// are still recognised as its own) and the deployment records. It runs once
// per database and returns how many deployments it imported.
func (s *Store) ImportLegacy(configDir string) (int, error) {
	if s == nil || configDir == "" {
		return 0, nil
	}
	if _, done, err := s.meta("legacy_imported"); err != nil || done {
		return 0, err
	}
	if _, ok, err := s.meta("instance"); err != nil {
		return 0, err
	} else if !ok {
		if b, err := os.ReadFile(filepath.Join(configDir, "instance")); err == nil && len(strings.TrimSpace(string(b))) >= 8 {
			if err := s.setMeta("instance", strings.TrimSpace(string(b))); err != nil {
				return 0, err
			}
		}
	}
	n := 0
	files, _ := filepath.Glob(filepath.Join(configDir, "deployments", "*.json"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var rec struct {
			Name     string        `json:"name"`
			Token    string        `json:"token"`
			Network  string        `json:"network"`
			Topology spec.Topology `json:"topology"`
		}
		if json.Unmarshal(b, &rec) != nil || rec.Name == "" {
			continue
		}
		slug := strings.TrimSuffix(filepath.Base(f), ".json")
		if _, exists, err := s.Deployment(slug); err != nil || exists {
			continue
		}
		d := Deployment{Name: rec.Name, Slug: slug, Token: rec.Token, Network: rec.Network, Phase: "running", Topology: rec.Topology.Normalized()}
		if _, err := s.Save(d); err != nil {
			return n, err
		}
		n++
	}
	return n, s.setMeta("legacy_imported", time.Now().UTC().Format(time.RFC3339))
}
