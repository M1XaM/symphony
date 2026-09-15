package control

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Event is one state change announced on /events.
type Event struct {
	Seq  int64  `json:"seq"`
	Ts   int64  `json:"ts"`
	Kind string `json:"kind"` // fault.set | fault.expired | health.changed | config.set | ...
	Data any    `json:"data,omitempty"`
}

const eventRing = 256

// Events fans state changes out to SSE subscribers and keeps a short ring
// so a subscriber that reconnects can ask for what it missed (?since=seq).
type Events struct {
	mu   sync.Mutex
	seq  int64
	ring []Event
	subs map[chan Event]struct{}
}

func NewEvents() *Events {
	return &Events{subs: map[chan Event]struct{}{}}
}

// Publish records and broadcasts an event. Slow subscribers lose events
// rather than blocking the publisher.
func (e *Events) Publish(kind string, data any) {
	e.mu.Lock()
	e.seq++
	ev := Event{Seq: e.seq, Ts: time.Now().UnixMilli(), Kind: kind, Data: data}
	if len(e.ring) >= eventRing {
		e.ring = e.ring[1:]
	}
	e.ring = append(e.ring, ev)
	for ch := range e.subs {
		select {
		case ch <- ev:
		default:
		}
	}
	e.mu.Unlock()
}

// Recent returns the buffered events after seq.
func (e *Events) Recent(since int64) []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []Event
	for _, ev := range e.ring {
		if ev.Seq > since {
			out = append(out, ev)
		}
	}
	return out
}

func (e *Events) subscribe() chan Event {
	ch := make(chan Event, 64)
	e.mu.Lock()
	e.subs[ch] = struct{}{}
	e.mu.Unlock()
	return ch
}

func (e *Events) unsubscribe(ch chan Event) {
	e.mu.Lock()
	delete(e.subs, ch)
	e.mu.Unlock()
}

// Handler serves GET /events as a text/event-stream. Without an Accept of
// text/event-stream (or with ?since=) it answers the buffered events as
// JSON, which is easier to poll.
func (e *Events) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var since int64
		fmt.Sscan(r.URL.Query().Get("since"), &since)
		if r.Header.Get("Accept") != "text/event-stream" {
			w.Header().Set("Content-Type", "application/json")
			evs := e.Recent(since)
			if evs == nil {
				evs = []Event{}
			}
			_ = json.NewEncoder(w).Encode(evs)
			return
		}
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		ch := e.subscribe()
		defer e.unsubscribe(ch)
		for _, ev := range e.Recent(since) {
			writeSSE(w, ev)
		}
		fl.Flush()
		keep := time.NewTicker(15 * time.Second)
		defer keep.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case ev := <-ch:
				writeSSE(w, ev)
				fl.Flush()
			case <-keep.C:
				fmt.Fprint(w, ": keepalive\n\n")
				fl.Flush()
			}
		}
	}
}

func writeSSE(w http.ResponseWriter, ev Event) {
	b, _ := json.Marshal(ev)
	fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Kind, b)
}
