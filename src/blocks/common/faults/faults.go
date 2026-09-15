// Package faults stores the faults injected into a block through /control and
// decides, per request, what they do to it. Faults are scoped by a Target
// (nothing = the whole block, or an edge, a direction, a peer, a topic, a
// table) and may expire on their own.
package faults

import (
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"time"
)

// Kind is the class of fault.
type Kind string

const (
	Drop      Kind = "drop"      // rate: swallow the request / do not send
	Error     Kind = "error"     // rate, status: answer with an error
	Latency   Kind = "latency"   // ms, jitter_ms, rate: delay
	Timeout   Kind = "timeout"   // rate: hang until the caller gives up
	Slow      Kind = "slow"      // bytes_per_sec: throttle the body
	Partition Kind = "partition" // peers: refuse traffic to/from them
	Corrupt   Kind = "corrupt"   // rate: malformed body
	Duplicate Kind = "duplicate" // rate: send/deliver twice
	Reorder   Kind = "reorder"   // window: shuffle deliveries (broker)
	Reject    Kind = "reject"    // rate: refuse connections (database)
)

var Kinds = []Kind{Drop, Error, Latency, Timeout, Slow, Partition, Corrupt, Duplicate, Reorder, Reject}

func (k Kind) Valid() bool {
	for _, v := range Kinds {
		if k == v {
			return true
		}
	}
	return false
}

// Target scopes a fault. Every non-empty field must match the request's
// scope for the fault to apply.
type Target struct {
	Edge      string `json:"edge,omitempty"`
	Direction string `json:"direction,omitempty"` // in | out
	Peer      string `json:"peer,omitempty"`
	Topic     string `json:"topic,omitempty"`
	Table     string `json:"table,omitempty"`
}

func (t Target) matches(s Scope) bool {
	if t.Edge != "" && t.Edge != s.Edge {
		return false
	}
	if t.Direction != "" && t.Direction != s.Direction {
		return false
	}
	if t.Peer != "" && t.Peer != s.Peer {
		return false
	}
	if t.Topic != "" && t.Topic != s.Topic {
		return false
	}
	if t.Table != "" && t.Table != s.Table {
		return false
	}
	return true
}

// Scope is what a request looks like to the matcher.
type Scope struct {
	Edge      string
	Direction string
	Peer      string
	Topic     string
	Table     string
}

// Fault is one injected fault.
type Fault struct {
	ID        string         `json:"id"`
	Kind      Kind           `json:"kind"`
	Target    Target         `json:"target"`
	Params    map[string]any `json:"params,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	ExpiresAt time.Time      `json:"expires_at,omitempty"` // zero = until cleared
}

// RemainingMs is the time left before the fault clears itself, -1 for never.
func (f Fault) RemainingMs() int64 {
	if f.ExpiresAt.IsZero() {
		return -1
	}
	d := time.Until(f.ExpiresAt).Milliseconds()
	if d < 0 {
		return 0
	}
	return d
}

func (f Fault) num(key string, def float64) float64 {
	switch v := f.Params[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return def
}

// Store keeps the active faults and expires them.
type Store struct {
	mu       sync.Mutex
	faults   map[string]*Fault
	timers   map[string]*time.Timer
	seq      int
	onChange func(event string, f Fault)
}

func NewStore(onChange func(event string, f Fault)) *Store {
	return &Store{faults: map[string]*Fault{}, timers: map[string]*time.Timer{}, onChange: onChange}
}

// Set adds a fault and returns it with its id filled in.
func (s *Store) Set(kind Kind, target Target, params map[string]any, duration time.Duration) (Fault, error) {
	if !kind.Valid() {
		return Fault{}, fmt.Errorf("faults: unknown kind %q", kind)
	}
	if target.Direction != "" && target.Direction != "in" && target.Direction != "out" {
		return Fault{}, fmt.Errorf("faults: direction must be in or out, got %q", target.Direction)
	}
	if params == nil {
		params = map[string]any{}
	}
	s.mu.Lock()
	s.seq++
	f := &Fault{ID: fmt.Sprintf("f-%d", s.seq), Kind: kind, Target: target, Params: params, CreatedAt: time.Now()}
	if duration > 0 {
		f.ExpiresAt = f.CreatedAt.Add(duration)
		id := f.ID
		s.timers[id] = time.AfterFunc(duration, func() { s.expire(id) })
	}
	s.faults[f.ID] = f
	snap := *f
	s.mu.Unlock()
	if s.onChange != nil {
		s.onChange("fault.set", snap)
	}
	return snap, nil
}

func (s *Store) expire(id string) {
	s.mu.Lock()
	f, ok := s.faults[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	delete(s.faults, id)
	delete(s.timers, id)
	snap := *f
	s.mu.Unlock()
	if s.onChange != nil {
		s.onChange("fault.expired", snap)
	}
}

// Clear removes one fault; false when it was not there.
func (s *Store) Clear(id string) bool {
	s.mu.Lock()
	f, ok := s.faults[id]
	if !ok {
		s.mu.Unlock()
		return false
	}
	if t := s.timers[id]; t != nil {
		t.Stop()
	}
	delete(s.faults, id)
	delete(s.timers, id)
	snap := *f
	s.mu.Unlock()
	if s.onChange != nil {
		s.onChange("fault.cleared", snap)
	}
	return true
}

// ClearAll removes every fault and returns how many there were.
func (s *Store) ClearAll() int {
	s.mu.Lock()
	ids := make([]string, 0, len(s.faults))
	for id := range s.faults {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.Clear(id)
	}
	return len(ids)
}

// List returns the active faults, oldest first.
func (s *Store) List() []Fault {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Fault, 0, len(s.faults))
	for _, f := range s.faults {
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Matching returns the faults whose target covers the scope.
func (s *Store) Matching(scope Scope) []Fault {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Fault
	for _, f := range s.faults {
		if f.Target.matches(scope) {
			out = append(out, *f)
		}
	}
	return out
}

// Decision is what the matching faults do to one request. Fields compose in
// the order partition -> reject -> drop -> error -> latency/timeout -> slow ->
// corrupt/duplicate; the caller applies them in that order.
type Decision struct {
	Partitioned bool
	Rejected    bool
	Dropped     bool
	ErrorStatus int // 0 = none
	Delay       time.Duration
	Hang        bool
	BytesPerSec int // 0 = unlimited
	Corrupt     bool
	Duplicate   bool
	// Cause names the fault that decided the outcome, e.g. "fault:drop".
	Cause string
}

// Affected reports whether any fault changed the request's fate.
func (d Decision) Affected() bool {
	return d.Partitioned || d.Rejected || d.Dropped || d.ErrorStatus != 0 || d.Delay > 0 || d.Hang || d.BytesPerSec > 0 || d.Corrupt || d.Duplicate
}

// Decide rolls the dice for one request against the scope's faults.
func (s *Store) Decide(scope Scope, rng *rand.Rand) Decision {
	var d Decision
	for _, f := range s.Matching(scope) {
		rate := f.num("rate", 1)
		hit := rate >= 1 || rng.Float64() < rate
		switch f.Kind {
		case Partition:
			if peers, ok := f.Params["peers"].([]any); ok {
				for _, p := range peers {
					if ps, _ := p.(string); ps == scope.Peer {
						d.Partitioned = true
						d.Cause = "fault:partition"
					}
				}
			} else {
				d.Partitioned = true
				d.Cause = "fault:partition"
			}
		case Reject:
			if hit {
				d.Rejected = true
				d.Cause = "fault:reject"
			}
		case Drop:
			if hit {
				d.Dropped = true
				d.Cause = "fault:drop"
			}
		case Error:
			if hit {
				d.ErrorStatus = int(f.num("status", 503))
				d.Cause = "fault:error"
			}
		case Latency:
			if hit {
				ms := f.num("ms", 100)
				if j := f.num("jitter_ms", 0); j > 0 {
					ms += (rng.Float64()*2 - 1) * j
				}
				if ms > 0 {
					d.Delay += time.Duration(ms * float64(time.Millisecond))
				}
				if d.Cause == "" {
					d.Cause = "fault:latency"
				}
			}
		case Timeout:
			if hit {
				d.Hang = true
				d.Cause = "fault:timeout"
			}
		case Slow:
			bps := int(f.num("bytes_per_sec", 1024))
			if bps > 0 && (d.BytesPerSec == 0 || bps < d.BytesPerSec) {
				d.BytesPerSec = bps
			}
			if d.Cause == "" {
				d.Cause = "fault:slow"
			}
		case Corrupt:
			if hit {
				d.Corrupt = true
				d.Cause = "fault:corrupt"
			}
		case Duplicate:
			if hit {
				d.Duplicate = true
				if d.Cause == "" {
					d.Cause = "fault:duplicate"
				}
			}
		case Reorder:
			// handled by the broker's delivery loop through Matching()
		}
	}
	return d
}
