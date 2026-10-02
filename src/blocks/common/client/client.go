// Package client is the outbound side of a link: it picks a target replica,
// applies the outbound faults, propagates the envelope, retries with backoff
// and keeps a circuit breaker per target. One Client per outbound edge.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"symphony/blocks/common/faults"
	"symphony/blocks/common/links"
	"symphony/blocks/common/metrics"
	"symphony/blocks/common/wire"
)

// Settings are the resilience knobs, read from config before each call so
// hot changes apply immediately.
type Settings struct {
	Timeout          time.Duration
	Retries          int
	BackoffBase      time.Duration
	BreakerThreshold int           // consecutive failures that open the breaker; 0 = disabled
	BreakerOpen      time.Duration // how long it stays open
}

// Client sends requests over one outbound link.
type Client struct {
	Self     string // this node's id
	Link     links.Link
	Settings func() Settings
	Faults   *faults.Store
	Metrics  *metrics.Registry
	HTTP     *http.Client

	rng      *rand.Rand
	rngMu    sync.Mutex
	rr       uint32
	breakers []*breaker
}

// New builds a client for a link with a shared HTTP transport.
func New(self string, link links.Link, settings func() Settings, fs *faults.Store, reg *metrics.Registry, httpc *http.Client) *Client {
	c := &Client{Self: self, Link: link, Settings: settings, Faults: fs, Metrics: reg, HTTP: httpc,
		rng: rand.New(rand.NewSource(time.Now().UnixNano()))}
	for range link.Targets {
		c.breakers = append(c.breakers, &breaker{})
	}
	return c
}

// DefaultTransport is tuned for many small keep-alive connections.
func DefaultTransport() *http.Client {
	return &http.Client{Transport: &http.Transport{
		MaxIdleConns:        256,
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     60 * time.Second,
		DialContext:         (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
	}}
}

// Outcome classifies a call for metrics.
type Outcome string

const (
	OutcomeOK      Outcome = "ok"
	OutcomeError   Outcome = "error"   // peer answered 5xx / 4xx
	OutcomeTimeout Outcome = "timeout" // deadline or transport timeout
	OutcomeDropped Outcome = "dropped" // an outbound fault swallowed it
	OutcomeRefused Outcome = "refused" // connection refused / partitioned / breaker open
)

// Result of a call.
type Result struct {
	Status   int
	Response wire.Response
	Outcome  Outcome
	Attempts int
	Target   string
	Err      error
}

// Call sends req on the link with meta's saga/deadline, handling faults,
// retries and the breaker. It always returns a Result; Err is set on failure.
func (c *Client) Call(ctx context.Context, meta wire.Meta, req wire.Request) Result {
	st := c.Settings()
	if c.Link.TimeoutMs > 0 {
		st.Timeout = time.Duration(c.Link.TimeoutMs) * time.Millisecond
	}
	if c.Link.Retries > 0 {
		st.Retries = c.Link.Retries
	}
	scope := faults.Scope{Edge: c.Link.Edge, Direction: "out", Peer: c.Link.Peer(c.Self), Topic: c.Link.Topic}
	total := c.Metrics.Counter("link_requests_total", metrics.Labels{"edge": c.Link.Edge, "dir": "out"})
	total.Inc()
	hist := c.Metrics.Histogram("link_latency_ms", metrics.Labels{"edge": c.Link.Edge, "dir": "out"})

	deadline := meta.Deadline
	if st.Timeout > 0 {
		if d := time.Now().Add(st.Timeout); deadline.IsZero() || d.Before(deadline) {
			deadline = d
		}
	}
	meta.Deadline = deadline
	meta.Edge = c.Link.Edge
	meta.Hop++
	req.Trace = append(req.Trace, wire.Hop{Node: c.Self, Edge: c.Link.Edge, Ts: time.Now().UnixMilli()})
	if meta.Hop > wire.MaxHops {
		return c.finish(Result{Outcome: OutcomeError, Err: errors.New("hop limit exceeded (loop?)")}, total.Labels, 0)
	}

	body, _ := json.Marshal(req)
	start := time.Now()
	var res Result
	attempts := st.Retries + 1
	for attempt := 1; attempt <= attempts; attempt++ {
		if time.Now().After(deadline) {
			res = Result{Outcome: OutcomeTimeout, Err: errors.New("deadline passed before send"), Attempts: attempt - 1}
			break
		}
		c.rngMu.Lock()
		d := c.Faults.Decide(scope, c.rng)
		c.rngMu.Unlock()
		if d.Partitioned {
			res = Result{Outcome: OutcomeRefused, Err: errors.New(d.Cause), Attempts: attempt}
			break
		}
		if d.Dropped {
			res = Result{Outcome: OutcomeDropped, Err: errors.New(d.Cause), Attempts: attempt}
			break
		}
		if d.Delay > 0 {
			if err := sleepCtx(ctx, min(d.Delay, time.Until(deadline))); err != nil {
				res = Result{Outcome: OutcomeTimeout, Err: err, Attempts: attempt}
				break
			}
		}
		meta.Attempt = attempt
		res = c.once(ctx, meta, body, deadline)
		res.Attempts = attempt
		if d.Duplicate && res.Outcome == OutcomeOK {
			// the network delivered it twice: fire and forget the copy
			go c.once(context.Background(), meta, body, deadline)
		}
		if res.Outcome == OutcomeOK || res.Outcome == OutcomeDropped {
			break
		}
		if res.Status >= 400 && res.Status < 500 && res.Status != 429 {
			break // the peer said the request itself is wrong; retrying will not help
		}
		if attempt < attempts {
			back := st.BackoffBase * time.Duration(1<<uint(attempt-1))
			back += time.Duration(c.rand() * float64(back) / 2)
			if err := sleepCtx(ctx, min(back, time.Until(deadline))); err != nil {
				res.Outcome = OutcomeTimeout
				res.Err = err
				break
			}
		}
	}
	elapsed := float64(time.Since(start).Microseconds()) / 1000
	hist.Observe(elapsed)
	return c.finish(res, total.Labels, res.Attempts)
}

func (c *Client) finish(res Result, labels metrics.Labels, attempts int) Result {
	c.Metrics.Counter("link_results_total", metrics.Labels{"edge": c.Link.Edge, "dir": "out", "result": string(res.Outcome)}).Inc()
	c.Metrics.Window("link_recent", metrics.Labels{"edge": c.Link.Edge, "dir": "out"}).Record(res.Outcome == OutcomeOK)
	if attempts > 1 {
		c.Metrics.Counter("link_retries_total", metrics.Labels{"edge": c.Link.Edge}).Add(int64(attempts - 1))
	}
	return res
}

func (c *Client) rand() float64 {
	c.rngMu.Lock()
	defer c.rngMu.Unlock()
	return c.rng.Float64()
}

// once performs a single HTTP attempt against the next healthy target.
func (c *Client) once(ctx context.Context, meta wire.Meta, body []byte, deadline time.Time) Result {
	idx, ok := c.pickTarget()
	if !ok {
		return Result{Outcome: OutcomeRefused, Err: errors.New("circuit breaker open for every target")}
	}
	target := c.Link.Targets[idx]
	br := c.breakers[idx]
	url := strings.TrimRight(target, "/") + "/in/" + c.Link.Edge

	cctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	hreq, err := http.NewRequestWithContext(cctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Result{Outcome: OutcomeError, Err: err, Target: target}
	}
	hreq.Header.Set("Content-Type", "application/json")
	wire.WriteMeta(hreq.Header, meta)

	resp, err := c.HTTP.Do(hreq)
	if err != nil {
		br.fail(c.Settings())
		out := OutcomeError
		var ne net.Error
		switch {
		case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
			out = OutcomeTimeout
		case strings.Contains(err.Error(), "connection refused"), strings.Contains(err.Error(), "no such host"):
			out = OutcomeRefused
		}
		return Result{Outcome: out, Err: err, Target: target}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var wr wire.Response
	_ = json.Unmarshal(raw, &wr)
	res := Result{Status: resp.StatusCode, Response: wr, Target: target}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		br.ok()
		res.Outcome = OutcomeOK
		if wr.Error != "" && wr.Cause == "fault:corrupt" {
			res.Outcome = OutcomeError
			res.Err = errors.New("corrupt response")
		}
		return res
	}
	br.fail(c.Settings())
	res.Outcome = OutcomeError
	if resp.StatusCode == http.StatusGatewayTimeout {
		res.Outcome = OutcomeTimeout
	}
	msg := wr.Error
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	res.Err = fmt.Errorf("%s: %s", target, msg)
	return res
}

// pickTarget round-robins over targets whose breaker allows a call.
func (c *Client) pickTarget() (int, bool) {
	n := len(c.Link.Targets)
	if n == 0 {
		return 0, false
	}
	start := int(atomic.AddUint32(&c.rr, 1)-1) % n
	for i := 0; i < n; i++ {
		idx := (start + i) % n
		if c.breakers[idx].allow() {
			return idx, true
		}
	}
	return 0, false
}

// TargetStates reports per-target breaker state for health.
func (c *Client) TargetStates() []map[string]any {
	out := make([]map[string]any, 0, len(c.Link.Targets))
	for i, t := range c.Link.Targets {
		out = append(out, map[string]any{"target": t, "breaker": c.breakers[i].state(), "failures": c.breakers[i].failures.Load()})
	}
	return out
}

// Reset closes every target's breaker and forgets its failures
// (state.reset): what a link went through before must not decide the first
// seconds of what is measured next.
func (c *Client) Reset() {
	for _, b := range c.breakers {
		b.ok()
	}
}

// TargetsUp counts targets whose breaker is closed.
func (c *Client) TargetsUp() int {
	n := 0
	for _, b := range c.breakers {
		if b.state() != "open" {
			n++
		}
	}
	return n
}

// breaker is a classic closed -> open -> half-open circuit breaker.
type breaker struct {
	mu        sync.Mutex
	failures  atomic.Int64
	openUntil time.Time
	halfOpen  bool
}

func (b *breaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.openUntil.IsZero() {
		return true
	}
	if time.Now().After(b.openUntil) {
		if b.halfOpen {
			return false // one trial in flight
		}
		b.halfOpen = true
		return true
	}
	return false
}

func (b *breaker) ok() {
	b.mu.Lock()
	b.failures.Store(0)
	b.openUntil = time.Time{}
	b.halfOpen = false
	b.mu.Unlock()
}

func (b *breaker) fail(st Settings) {
	n := b.failures.Add(1)
	if st.BreakerThreshold <= 0 {
		return
	}
	b.mu.Lock()
	if b.halfOpen || n >= int64(st.BreakerThreshold) {
		b.openUntil = time.Now().Add(st.BreakerOpen)
		b.halfOpen = false
	}
	b.mu.Unlock()
}

func (b *breaker) state() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case b.openUntil.IsZero():
		return "closed"
	case time.Now().After(b.openUntil):
		return "half-open"
	}
	return "open"
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
