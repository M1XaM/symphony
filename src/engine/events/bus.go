// Package events is the typed in-process event bus. The engine, runtime and
// metrics ticker publish here; internal/api subscribes once and forwards
// every event to the frontend through Wails' runtime.EventsEmit, using the
// event Type as the Wails event name.
package events

import (
	"sync"
	"time"
)

// Type is the event name as seen by the frontend (EventsOn("apply:progress")).
type Type string

const (
	ApplyProgress Type = "apply:progress" // ProgressPayload, one per operation state change
	EngineLog     Type = "engine:log"     // LogPayload, human-readable log line
	EnginePhase   Type = "engine:phase"   // PhasePayload, engine state machine transitions
	NodeStatus    Type = "node:status"    // NodeStatusPayload, per-node runtime state
	MetricsTick   Type = "metrics:tick"   // MetricsPayload, emitted every 500ms
	LinkHealth    Type = "link:health"    // LinkHealthPayload, one per deployment every 500ms
	HostStats     Type = "host:stats"     // HostStatsPayload, host machine usage every second
	FaultInjected Type = "fault:injected" // FaultPayload
)

// Event is the envelope delivered to subscribers.
type Event struct {
	Type    Type
	Time    time.Time
	Payload any
}

// ProgressPayload reports the lifecycle of one plan operation.
type ProgressPayload struct {
	Deployment string `json:"deployment"` // name of the topology the plan belongs to
	OpID       string `json:"op_id"`
	Index      int    `json:"index"` // 0-based position in the plan
	Count      int    `json:"count"` // total operations in the plan
	Kind       string `json:"kind"`
	Target     string `json:"target"`
	Class      string `json:"class"`
	Status     string `json:"status"` // started | done | failed
	Message    string `json:"message,omitempty"`
	Elapsed    int64  `json:"elapsed_ms"`
}

// LogPayload is a free-form log line.
type LogPayload struct {
	Level   string `json:"level"` // debug | info | warn | error
	Source  string `json:"source"`
	Message string `json:"message"`
	Ts      int64  `json:"ts"` // unix ms
}

// PhasePayload announces a deployment's phase change. Phase "gone" means the
// deployment was torn down and no longer exists.
type PhasePayload struct {
	Deployment string `json:"deployment"`
	Phase      string `json:"phase"`
	Message    string `json:"message,omitempty"`
}

// NodeStatusPayload is the runtime state of a topology node.
type NodeStatusPayload struct {
	Deployment string `json:"deployment"`
	NodeID     string `json:"node_id"`
	State      string `json:"state"` // pending | starting | healthy | failed | stopped | paused | removed
	Replicas   int    `json:"replicas"`
	Ready      int    `json:"ready"`
	Message    string `json:"message,omitempty"`
	UpdatedAt  int64  `json:"updated_at"`
}

// MetricsPayload is one sample from the metrics pipeline.
type MetricsPayload struct {
	Ts           int64   `json:"ts"`
	LatencyMs    float64 `json:"latency_ms"`
	P99Ms        float64 `json:"p99_ms"`
	ConsumerLag  int     `json:"consumer_lag"`
	SagasPerSec  float64 `json:"sagas_per_sec"`
	CPUPct       float64 `json:"cpu_pct"`
	ActiveFaults int     `json:"active_faults"`
}

// LinkLevel classifies a link's error rate for the map: ok (green), warn
// (yellow) and crit (red).
type LinkLevel string

const (
	LinkOK   LinkLevel = "ok"
	LinkWarn LinkLevel = "warn"
	LinkCrit LinkLevel = "crit"
)

// LinkHealthEntry is the sampled health of one edge.
type LinkHealthEntry struct {
	ErrorRate float64   `json:"error_rate"` // 0..1, share of requests/messages lost or failed
	Level     LinkLevel `json:"level"`
}

// LinkHealthPayload carries every edge of one deployment, keyed by edge id.
type LinkHealthPayload struct {
	Deployment string                     `json:"deployment"`
	Ts         int64                      `json:"ts"`
	Links      map[string]LinkHealthEntry `json:"links"`
}

// HostStatsPayload is one sample of the host machine's resource usage.
type HostStatsPayload struct {
	Ts       int64   `json:"ts"`
	CPUPct   float64 `json:"cpu_pct"`
	MemPct   float64 `json:"mem_pct"`
	MemUsed  uint64  `json:"mem_used_bytes"`
	MemTotal uint64  `json:"mem_total_bytes"`
	NetRxBps float64 `json:"net_rx_bps"` // bytes per second received, all interfaces
	NetTxBps float64 `json:"net_tx_bps"` // bytes per second sent, all interfaces
}

// FaultPayload records a fault being injected or cleared.
type FaultPayload struct {
	Deployment string `json:"deployment"`
	NodeID     string `json:"node_id"`
	Kind       string `json:"kind"`
	DurationMs int    `json:"duration_ms"`
	Active     bool   `json:"active"`
}

// logHistoryCap bounds the replay buffer. The frontend keeps its own cap; this
// only has to cover the window between process start and the webview attaching.
const logHistoryCap = 500

// Handler receives events. Handlers are invoked synchronously on the
// publisher's goroutine; keep them fast (the Wails forwarder only serialises).
type Handler func(Event)

// Bus fans events out to subscribers. Safe for concurrent use.
type Bus struct {
	mu      sync.RWMutex
	next    int
	subs    map[int]Handler
	history []LogPayload
}

func NewBus() *Bus {
	return &Bus{subs: map[int]Handler{}}
}

// Subscribe registers a handler and returns a function that removes it.
func (b *Bus) Subscribe(h Handler) (unsubscribe func()) {
	b.mu.Lock()
	id := b.next
	b.next++
	b.subs[id] = h
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
	}
}

// Publish delivers an event to every subscriber.
func (b *Bus) Publish(t Type, payload any) {
	ev := Event{Type: t, Time: time.Now(), Payload: payload}
	b.mu.RLock()
	handlers := make([]Handler, 0, len(b.subs))
	for _, h := range b.subs {
		handlers = append(handlers, h)
	}
	b.mu.RUnlock()
	for _, h := range handlers {
		h(ev)
	}
}

// Log is a convenience for publishing an EngineLog event. Lines are also kept
// in a bounded history: most of the interesting startup logging happens before
// the webview exists, and it would otherwise be lost.
func (b *Bus) Log(level, source, msg string) {
	line := LogPayload{Level: level, Source: source, Message: msg, Ts: time.Now().UnixMilli()}
	b.mu.Lock()
	if len(b.history) >= logHistoryCap {
		b.history = append(b.history[:0], b.history[len(b.history)-logHistoryCap+1:]...)
	}
	b.history = append(b.history, line)
	b.mu.Unlock()
	b.Publish(EngineLog, line)
}

// LogHistory returns the buffered log lines, oldest first.
func (b *Bus) LogHistory() []LogPayload {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append(make([]LogPayload, 0, len(b.history)), b.history...)
}
