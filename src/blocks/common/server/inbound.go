// The data path's inbound side: one endpoint per inbound edge, wrapped in the
// middleware every block shares (faults, the stall gate, the envelope,
// metrics), and the health check derived from a link's recent results.

package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"symphony/blocks/common/faults"
	"symphony/blocks/common/health"
	"symphony/blocks/common/links"
	"symphony/blocks/common/metrics"
	"symphony/blocks/common/wire"
)

// Inbound is what an inbound handler receives.
type Inbound struct {
	Link    links.Link
	Meta    wire.Meta
	Request wire.Request
	Ctx     context.Context
}

// Reply is what a handler answers: a result and, when this node decides a
// saga's fate, the outcome (ok | failed | compensated | pending).
type Reply struct {
	Result  json.RawMessage
	Outcome string
}

// Handler processes one inbound request and returns the reply or an error.
// A returned *Reject is answered with its status; any other error is 500.
type Handler func(in Inbound) (Reply, error)

// Reject is an error with an HTTP status and a cause. Outcome, when set,
// still tells the caller how the saga ended (e.g. compensated).
type Reject struct {
	Status  int
	Msg     string
	Cause   string
	Outcome string
}

func (r *Reject) Error() string { return r.Msg }

// HandleIn registers the data-path endpoint for an inbound edge, wrapped in
// the common middleware: stall gate, inbound faults, envelope, metrics.
func (b *Block) HandleIn(link links.Link, h Handler) {
	total := b.Metrics.Counter("link_requests_total", metrics.Labels{"edge": link.Edge, "dir": "in"})
	hist := b.Metrics.Histogram("link_latency_ms", metrics.Labels{"edge": link.Edge, "dir": "in"})
	recent := b.Metrics.Window("link_recent", metrics.Labels{"edge": link.Edge, "dir": "in"})
	result := func(r string) *metrics.Counter {
		recent.Record(r == "ok")
		return b.Metrics.Counter("link_results_total", metrics.Labels{"edge": link.Edge, "dir": "in", "result": r})
	}
	peer := link.Peer(b.Node)
	b.Data.HandleFunc("POST /in/"+link.Edge, func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		total.Inc()
		meta := wire.ReadMeta(r.Header)
		scope := faults.Scope{Edge: link.Edge, Direction: "in", Peer: peer, Topic: link.Topic}
		b.rngMu.Lock()
		d := b.Faults.Decide(scope, b.rng)
		b.rngMu.Unlock()

		fail := func(status int, msg, cause, res, outcome string) {
			result(res).Inc()
			hist.Observe(msSince(start))
			writeResponse(w, status, wire.Response{Saga: meta.Saga, Node: b.Node, Edge: link.Edge, Error: msg, Cause: cause, Outcome: outcome})
		}
		switch {
		case d.Partitioned, d.Rejected, d.Dropped:
			// close without answering: to the caller this is a reset
			result("dropped").Inc()
			hist.Observe(msSince(start))
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					conn.Close()
					return
				}
			}
			w.WriteHeader(http.StatusBadGateway)
			return
		case d.Hang:
			result("timeout").Inc()
			<-r.Context().Done()
			return
		case d.ErrorStatus != 0:
			fail(d.ErrorStatus, "injected error", d.Cause, "fault", "")
			return
		}
		if d.Delay > 0 {
			select {
			case <-time.After(d.Delay):
			case <-r.Context().Done():
				result("timeout").Inc()
				return
			}
		}
		if err := b.Gate.Wait(r.Context()); err != nil {
			result("timeout").Inc()
			return
		}
		if !meta.Deadline.IsZero() && time.Now().After(meta.Deadline) {
			fail(http.StatusGatewayTimeout, "caller deadline already passed", "deadline", "timeout", "")
			return
		}

		var req wire.Request
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			fail(http.StatusBadRequest, "bad envelope: "+err.Error(), "envelope", "error", "")
			return
		}
		if req.Saga == "" {
			req.Saga = meta.Saga
		}
		if meta.Saga == "" {
			meta.Saga = req.Saga
		}
		req.Trace = append(req.Trace, wire.Hop{Node: b.Node, Edge: link.Edge, Ts: time.Now().UnixMilli()})

		ctx := r.Context()
		if !meta.Deadline.IsZero() {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, meta.Deadline)
			defer cancel()
		}
		reply, err := h(Inbound{Link: link, Meta: meta, Request: req, Ctx: ctx})
		if err != nil {
			var rj *Reject
			if errors.As(err, &rj) {
				res := "error"
				if rj.Status == http.StatusGatewayTimeout {
					res = "timeout"
				}
				fail(rj.Status, rj.Msg, rj.Cause, res, rj.Outcome)
				return
			}
			fail(http.StatusInternalServerError, err.Error(), "handler", "error", "")
			return
		}
		result("ok").Inc()
		hist.Observe(msSince(start))
		resp := wire.Response{Saga: req.Saga, Node: b.Node, Result: reply.Result, Trace: req.Trace, Outcome: reply.Outcome}
		if d.Corrupt {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"saga":"`+req.Saga+`","node":"`+b.Node+`","error":"corrupted","cause":"fault:corrupt","result":{"garb`)
			return
		}
		if d.BytesPerSec > 0 {
			body, _ := json.Marshal(resp)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			trickle(w, r.Context(), body, d.BytesPerSec)
			return
		}
		writeResponse(w, http.StatusOK, resp)
	})
	b.Health.Register("link:"+link.Edge+":in", b.linkCheck(link.Edge, "in"))
}

// trickle writes body at roughly bytesPerSec.
func trickle(w http.ResponseWriter, ctx context.Context, body []byte, bps int) {
	fl, _ := w.(http.Flusher)
	chunk := max(bps/10, 1)
	for off := 0; off < len(body); off += chunk {
		end := min(off+chunk, len(body))
		if _, err := w.Write(body[off:end]); err != nil {
			return
		}
		if fl != nil {
			fl.Flush()
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return
		}
	}
}

func writeResponse(w http.ResponseWriter, status int, resp wire.Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

func msSince(t time.Time) float64 { return float64(time.Since(t).Microseconds()) / 1000 }

// linkCheck derives a health check from a link's recent results (the last
// 64 requests), so a link that broke a moment ago shows up even after an
// hour of good traffic, and one that recovered clears quickly.
func (b *Block) linkCheck(edge, dir string) health.Func {
	return func() health.Check {
		total := b.Metrics.Counter("link_requests_total", metrics.Labels{"edge": edge, "dir": dir}).Value()
		okc := b.Metrics.Counter("link_results_total", metrics.Labels{"edge": edge, "dir": dir, "result": "ok"}).Value()
		rate, n := b.Metrics.Window("link_recent", metrics.Labels{"edge": edge, "dir": dir}).Rate()
		detail := map[string]any{"requests": total, "ok": okc, "recent_error_rate": float64(int(rate*1000)) / 1000, "recent_n": n}
		c := health.Check{Status: health.Healthy, Detail: detail}
		if n >= 5 {
			switch {
			case rate >= b.Config.Float("health.link_fail_rate", 0.5):
				c.Status = health.Failing
			case rate >= b.Config.Float("health.link_warn_rate", 0.05):
				c.Status = health.Degraded
			}
		}
		if dir == "out" {
			if cl, ok := b.Out(edge); ok {
				detail["targets"] = len(cl.Link.Targets)
				detail["targets_up"] = cl.TargetsUp()
				if cl.TargetsUp() == 0 {
					c.Status = health.Failing
					detail["cause"] = "every target's breaker is open"
				}
			}
		}
		return c
	}
}
