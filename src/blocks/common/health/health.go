// Package health aggregates the checks a block registers into the document
// served on /health. Every check reports healthy, degraded or failing with
// a detail map; the document's status is the worst of them.
package health

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// Status of a check or of the whole block.
type Status string

const (
	Healthy  Status = "healthy"
	Degraded Status = "degraded"
	Failing  Status = "failing"
)

func rank(s Status) int {
	switch s {
	case Healthy:
		return 0
	case Degraded:
		return 1
	}
	return 2
}

// Worst returns the most severe of the statuses.
func Worst(ss ...Status) Status {
	out := Healthy
	for _, s := range ss {
		if rank(s) > rank(out) {
			out = s
		}
	}
	return out
}

// Check is one line of the health document.
type Check struct {
	Name   string         `json:"name"`
	Status Status         `json:"status"`
	Detail map[string]any `json:"detail,omitempty"`
}

// Func computes a check on demand.
type Func func() Check

// Document is what /health answers.
type Document struct {
	Node     string  `json:"node"`
	Kind     string  `json:"kind"`
	Replica  int     `json:"replica"`
	UptimeMs int64   `json:"uptime_ms"`
	Status   Status  `json:"status"`
	Checks   []Check `json:"checks"`
	Version  string  `json:"version"`
}

// Registry collects the checks.
type Registry struct {
	Node, Kind, Version string
	Replica             int
	start               time.Time

	mu    sync.Mutex
	names []string
	funcs map[string]Func
}

func NewRegistry(node, kind, version string, replica int) *Registry {
	return &Registry{Node: node, Kind: kind, Version: version, Replica: replica, start: time.Now(), funcs: map[string]Func{}}
}

// Register adds or replaces a check. Order of first registration is kept.
func (r *Registry) Register(name string, fn Func) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.funcs[name]; !ok {
		r.names = append(r.names, name)
	}
	r.funcs[name] = fn
}

// Document evaluates every check.
func (r *Registry) Document() Document {
	r.mu.Lock()
	names := append([]string(nil), r.names...)
	funcs := make([]Func, 0, len(names))
	for _, n := range names {
		funcs = append(funcs, r.funcs[n])
	}
	r.mu.Unlock()

	d := Document{Node: r.Node, Kind: r.Kind, Replica: r.Replica, Version: r.Version, UptimeMs: time.Since(r.start).Milliseconds(), Status: Healthy, Checks: []Check{}}
	for i, fn := range funcs {
		c := fn()
		if c.Name == "" {
			c.Name = names[i]
		}
		if c.Status == "" {
			c.Status = Healthy
		}
		d.Checks = append(d.Checks, c)
		d.Status = Worst(d.Status, c.Status)
	}
	return d
}

// Handler serves GET /health. With ?strict=1 a non-healthy block answers
// 503, which is what the container HEALTHCHECK looks at; without it the
// document is always 200 so the engine can read the reason.
func (r *Registry) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		d := r.Document()
		w.Header().Set("Content-Type", "application/json")
		if req.URL.Query().Get("strict") != "" && d.Status != Healthy {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(d)
	}
}
