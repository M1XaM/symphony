// Package metrics is a small registry of counters and latency histograms
// exposed on the admin port as JSON (what the engine scrapes) and in
// Prometheus text format (for anything else).
package metrics

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Labels are a small ordered set of key=value pairs.
type Labels map[string]string

// Key is the labels in a canonical order, for use as a map key.
func (l Labels) Key() string {
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(l[k])
	}
	return b.String()
}

// Counter is a monotonically increasing integer.
type Counter struct {
	Name   string
	Labels Labels
	v      atomic.Int64
}

func (c *Counter) Inc()         { c.v.Add(1) }
func (c *Counter) Add(n int64)  { c.v.Add(n) }
func (c *Counter) Value() int64 { return c.v.Load() }
func (c *Counter) Set(n int64)  { c.v.Store(n) } // for gauges kept as counters
func (c *Counter) Reset()       { c.v.Store(0) }

// Buckets are the histogram upper bounds in milliseconds.
var Buckets = []float64{1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000}

const ringSize = 2048

// DefaultWindow is how far back a snapshot's percentiles look unless the
// reader asks for another window.
const DefaultWindow = 5 * time.Second

type sample struct {
	at int64 // unix ms
	ms float64
}

// Histogram records latencies: cumulative buckets for exposition plus a ring
// of recent samples, each with the time it was taken, for accurate
// percentiles over a window that ends now. Percentiles over everything since
// the start would still show a fault minutes after it ended.
type Histogram struct {
	Name   string
	Labels Labels

	mu     sync.Mutex
	counts []int64
	count  int64
	sum    float64
	ring   []sample
	pos    int
	now    func() time.Time // the clock, replaced in tests
}

func (h *Histogram) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

func (h *Histogram) Observe(ms float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.count++
	h.sum += ms
	for i, b := range Buckets {
		if ms <= b {
			h.counts[i]++
			break
		}
	}
	s := sample{at: h.clock().UnixMilli(), ms: ms}
	if len(h.ring) < ringSize {
		h.ring = append(h.ring, s)
	} else {
		h.ring[h.pos] = s
		h.pos = (h.pos + 1) % ringSize
	}
}

// Snapshot is the exported form of a histogram. Count, SumMs and Buckets
// cover everything since the block started; the percentiles and Max cover
// the Recent samples of the last WindowMs (zero when there were none).
type HistogramSnapshot struct {
	Name     string           `json:"name"`
	Labels   Labels           `json:"labels,omitempty"`
	Count    int64            `json:"count"`
	SumMs    float64          `json:"sum_ms"`
	WindowMs int64            `json:"window_ms"`
	Recent   int              `json:"recent"`
	P50      float64          `json:"p50_ms"`
	P90      float64          `json:"p90_ms"`
	P99      float64          `json:"p99_ms"`
	Max      float64          `json:"max_ms"`
	Buckets  map[string]int64 `json:"buckets"`
}

func (h *Histogram) snapshot(window time.Duration) HistogramSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := HistogramSnapshot{Name: h.Name, Labels: h.Labels, Count: h.count, SumMs: round1(h.sum), WindowMs: window.Milliseconds(), Buckets: map[string]int64{}}
	var cum int64
	for i, b := range Buckets {
		cum += h.counts[i]
		s.Buckets[fmt.Sprintf("%g", b)] = cum
	}
	s.Buckets["+Inf"] = h.count
	since := h.clock().Add(-window).UnixMilli()
	var recent []float64
	for _, smp := range h.ring {
		if smp.at >= since {
			recent = append(recent, smp.ms)
		}
	}
	if s.Recent = len(recent); s.Recent > 0 {
		sort.Float64s(recent)
		s.P50 = round1(pct(recent, 0.50))
		s.P90 = round1(pct(recent, 0.90))
		s.P99 = round1(pct(recent, 0.99))
		s.Max = round1(recent[len(recent)-1])
	}
	return s
}

func (h *Histogram) reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.counts {
		h.counts[i] = 0
	}
	h.count, h.sum, h.ring, h.pos = 0, 0, nil, 0
}

func pct(sorted []float64, p float64) float64 {
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	return sorted[idx]
}

// Registry holds every metric of a block.
type Registry struct {
	mu    sync.Mutex
	ctrs  map[string]*Counter
	hists map[string]*Histogram
	wins  map[string]*Window
	order []string
}

func NewRegistry() *Registry {
	return &Registry{ctrs: map[string]*Counter{}, hists: map[string]*Histogram{}}
}

// Counter returns the counter with this name and labels, creating it.
func (r *Registry) Counter(name string, labels Labels) *Counter {
	key := "c:" + name + "{" + labels.Key() + "}"
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.ctrs[key]; ok {
		return c
	}
	c := &Counter{Name: name, Labels: labels}
	r.ctrs[key] = c
	r.order = append(r.order, key)
	return c
}

// Histogram returns the histogram with this name and labels, creating it.
func (r *Registry) Histogram(name string, labels Labels) *Histogram {
	key := "h:" + name + "{" + labels.Key() + "}"
	r.mu.Lock()
	defer r.mu.Unlock()
	if h, ok := r.hists[key]; ok {
		return h
	}
	h := &Histogram{Name: name, Labels: labels, counts: make([]int64, len(Buckets))}
	r.hists[key] = h
	r.order = append(r.order, key)
	return h
}

// CounterSnapshot is the exported form of a counter.
type CounterSnapshot struct {
	Name   string `json:"name"`
	Labels Labels `json:"labels,omitempty"`
	Value  int64  `json:"value"`
}

// Document is the JSON served on /metrics.
type Document struct {
	Ts         int64               `json:"ts"`
	Counters   []CounterSnapshot   `json:"counters"`
	Histograms []HistogramSnapshot `json:"histograms"`
}

// Snapshot exports every metric, with percentiles over DefaultWindow.
func (r *Registry) Snapshot(ts int64) Document {
	return r.SnapshotWindow(ts, DefaultWindow)
}

// SnapshotWindow is Snapshot with percentiles over the given window.
func (r *Registry) SnapshotWindow(ts int64, window time.Duration) Document {
	if window <= 0 {
		window = DefaultWindow
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	d := Document{Ts: ts, Counters: []CounterSnapshot{}, Histograms: []HistogramSnapshot{}}
	for _, key := range r.order {
		if c, ok := r.ctrs[key]; ok {
			d.Counters = append(d.Counters, CounterSnapshot{Name: c.Name, Labels: c.Labels, Value: c.Value()})
		}
		if h, ok := r.hists[key]; ok {
			d.Histograms = append(d.Histograms, h.snapshot(window))
		}
	}
	return d
}

// Reset zeroes everything (state.reset).
func (r *Registry) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.ctrs {
		c.Reset()
	}
	for _, h := range r.hists {
		h.reset()
	}
	for _, w := range r.wins {
		w.mu.Lock()
		w.ring, w.pos, w.n, w.bad = [64]bool{}, 0, 0, 0
		w.mu.Unlock()
	}
}

// Prometheus renders the text exposition format.
func (r *Registry) Prometheus() string {
	d := r.Snapshot(0)
	var b strings.Builder
	for _, c := range d.Counters {
		fmt.Fprintf(&b, "%s%s %d\n", c.Name, promLabels(c.Labels), c.Value)
	}
	for _, h := range d.Histograms {
		for _, ub := range Buckets {
			l := Labels{}
			for k, v := range h.Labels {
				l[k] = v
			}
			l["le"] = fmt.Sprintf("%g", ub)
			fmt.Fprintf(&b, "%s_bucket%s %d\n", h.Name, promLabels(l), h.Buckets[fmt.Sprintf("%g", ub)])
		}
		l := Labels{}
		for k, v := range h.Labels {
			l[k] = v
		}
		l["le"] = "+Inf"
		fmt.Fprintf(&b, "%s_bucket%s %d\n", h.Name, promLabels(l), h.Count)
		fmt.Fprintf(&b, "%s_sum%s %g\n", h.Name, promLabels(h.Labels), h.SumMs)
		fmt.Fprintf(&b, "%s_count%s %d\n", h.Name, promLabels(h.Labels), h.Count)
	}
	return b.String()
}

func promLabels(l Labels) string {
	if len(l) == 0 {
		return ""
	}
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%q", k, l[k]))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }

// Window remembers the outcome of the last N events, for "is this link
// failing right now" checks that totals cannot answer.
type Window struct {
	mu   sync.Mutex
	ring [64]bool
	pos  int
	n    int
	bad  int
}

// Record adds one outcome.
func (w *Window) Record(ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.n == len(w.ring) {
		if !w.ring[w.pos] {
			w.bad--
		}
	} else {
		w.n++
	}
	w.ring[w.pos] = ok
	if !ok {
		w.bad++
	}
	w.pos = (w.pos + 1) % len(w.ring)
}

// Rate returns the failure share over the window and how many events it holds.
func (w *Window) Rate() (float64, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.n == 0 {
		return 0, 0
	}
	return float64(w.bad) / float64(w.n), w.n
}

// Window returns the window with this name and labels, creating it.
func (r *Registry) Window(name string, labels Labels) *Window {
	key := "w:" + name + "{" + labels.Key() + "}"
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.wins == nil {
		r.wins = map[string]*Window{}
	}
	if w, ok := r.wins[key]; ok {
		return w
	}
	w := &Window{}
	r.wins[key] = w
	return w
}
