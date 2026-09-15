package core

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"symphony/engine/spec"
)

// Store remembers applied deployments on disk so a restarted Symphony can
// adopt the containers it left running, and holds the instance id that
// labels everything this installation creates.
type Store struct {
	dir string
}

// NewStore uses <dir>/deployments and <dir>/instance. An empty dir gives a
// store that remembers nothing.
func NewStore(dir string) *Store { return &Store{dir: dir} }

// record is the on-disk shape of one deployment.
type record struct {
	Name     string        `json:"name"`
	Token    string        `json:"token"`
	Network  string        `json:"network"`
	Topology spec.Topology `json:"topology"`
}

// InstanceID returns the persistent owner id, creating it on first use.
func (s *Store) InstanceID() (string, error) {
	if s.dir == "" {
		return randomID(), nil
	}
	p := filepath.Join(s.dir, "instance")
	if b, err := os.ReadFile(p); err == nil && len(b) >= 8 {
		return string(b), nil
	}
	id := randomID()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return "", err
	}
	return id, os.WriteFile(p, []byte(id), 0o644)
}

func randomID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// NewToken mints an admin token for a deployment.
func NewToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Store) path(slug string) string {
	return filepath.Join(s.dir, "deployments", slug+".json")
}

func (s *Store) save(slug string, r record) error {
	if s.dir == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path(slug)), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(slug) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(slug))
}

func (s *Store) remove(slug string) {
	if s.dir != "" {
		_ = os.Remove(s.path(slug))
	}
}

func (s *Store) load(slug string) (record, error) {
	if s.dir == "" {
		return record{}, errors.New("no store")
	}
	b, err := os.ReadFile(s.path(slug))
	if err != nil {
		return record{}, err
	}
	var r record
	if err := json.Unmarshal(b, &r); err != nil {
		return record{}, fmt.Errorf("deployment record %s: %w", slug, err)
	}
	r.Topology = r.Topology.Normalized()
	return r, nil
}
