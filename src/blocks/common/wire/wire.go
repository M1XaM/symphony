// Package wire is the envelope every block speaks: JSON bodies plus a fixed
// set of headers that carry the saga id, the edge, the hop count, the
// caller's deadline and the retry attempt through every hop of a topology.
package wire

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

const (
	HeaderSaga     = "X-Symphony-Saga"
	HeaderEdge     = "X-Symphony-Edge"
	HeaderHop      = "X-Symphony-Hop"
	HeaderDeadline = "X-Symphony-Deadline" // unix milliseconds
	HeaderAttempt  = "X-Symphony-Attempt"
	HeaderOutcome  = "X-Symphony-Outcome" // ok | failed | compensated, set by the node that decides a saga's fate
	HeaderStep     = "X-Symphony-Step"
)

// MaxHops bounds a request's journey so a loop drawn on the canvas cannot
// spin forever.
const MaxHops = 32

// Hop is one entry of a request's trace.
type Hop struct {
	Node string `json:"node"`
	Edge string `json:"edge"`
	Ts   int64  `json:"ts"`
}

// Request is the body of every data-path call and every delivered message.
type Request struct {
	Saga    string          `json:"saga"`
	Step    string          `json:"step,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Trace   []Hop           `json:"trace,omitempty"`
}

// Response is the body of every data-path reply. Error and Cause are set on
// failures; Cause distinguishes an injected fault ("fault:drop") from a
// genuine one ("pool exhausted").
type Response struct {
	Saga   string          `json:"saga"`
	Node   string          `json:"node"`
	Result json.RawMessage `json:"result,omitempty"`
	Trace  []Hop           `json:"trace,omitempty"`
	Error  string          `json:"error,omitempty"`
	Edge   string          `json:"edge,omitempty"`
	Cause  string          `json:"cause,omitempty"`
	// Outcome is set by the node that decides a saga's fate: ok, failed,
	// compensated, or pending when the saga continues asynchronously (the
	// sink then reports the final outcome to the gateway).
	Outcome string `json:"outcome,omitempty"`
}

// Meta is the header part of the envelope.
type Meta struct {
	Saga     string
	Edge     string
	Step     string
	Hop      int
	Attempt  int
	Deadline time.Time // zero when the caller set none
}

// ReadMeta extracts the envelope headers from a request.
func ReadMeta(h http.Header) Meta {
	m := Meta{Saga: h.Get(HeaderSaga), Edge: h.Get(HeaderEdge), Step: h.Get(HeaderStep)}
	m.Hop, _ = strconv.Atoi(h.Get(HeaderHop))
	m.Attempt, _ = strconv.Atoi(h.Get(HeaderAttempt))
	if ms, err := strconv.ParseInt(h.Get(HeaderDeadline), 10, 64); err == nil && ms > 0 {
		m.Deadline = time.UnixMilli(ms)
	}
	return m
}

// WriteMeta sets the envelope headers.
func WriteMeta(h http.Header, m Meta) {
	h.Set(HeaderSaga, m.Saga)
	h.Set(HeaderEdge, m.Edge)
	if m.Step != "" {
		h.Set(HeaderStep, m.Step)
	}
	h.Set(HeaderHop, strconv.Itoa(m.Hop))
	h.Set(HeaderAttempt, strconv.Itoa(m.Attempt))
	if !m.Deadline.IsZero() {
		h.Set(HeaderDeadline, strconv.FormatInt(m.Deadline.UnixMilli(), 10))
	}
}

// Remaining is the time left until the deadline, or def when there is none.
func (m Meta) Remaining(def time.Duration) time.Duration {
	if m.Deadline.IsZero() {
		return def
	}
	return time.Until(m.Deadline)
}
