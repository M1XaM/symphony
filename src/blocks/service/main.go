// Command service is the universal service block: a bounded worker pool
// running a behaviour program for every inbound edge, with resilient
// outbound calls. It is the orchestrator, the participants and the
// choreography steps of every topology; only its links and program differ.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"symphony/blocks/common/behaviour"
	"symphony/blocks/common/control"
	"symphony/blocks/common/health"
	"symphony/blocks/common/metrics"
	"symphony/blocks/common/server"
	"symphony/blocks/common/wire"
)

type service struct {
	b *server.Block

	progMu  sync.RWMutex
	program behaviour.Program
	source  string // env | control | default

	poolMu  sync.Mutex
	pool    chan struct{}
	waiting atomic.Int64
	busy    atomic.Int64

	idemMu sync.Mutex
	idem   map[string]idemEntry
	idemQ  []string

	dupes *metrics.Counter
}

func main() {
	b, err := server.New("service")
	if err != nil {
		log.Fatal(err)
	}
	s := &service{b: b, idem: map[string]idemEntry{}}
	b.Config.Define("workers", 32.0)
	b.Config.Define("queue", 64.0)
	b.Config.Define("queue_timeout_ms", 2000.0)
	b.Config.Define("cpu_ms", 2.0)
	b.Config.Define("io_ms", 0.0)
	b.Config.Define("saga_reply_pending", false)
	s.resizePool(b.Config.Int("workers", 32))
	b.Config.Watch(func(key string, v any) {
		if key == "workers" {
			s.resizePool(b.Config.Int("workers", 32))
		}
	})
	s.dupes = b.Metrics.Counter("duplicates_total", nil)

	if err := s.loadProgram(); err != nil {
		log.Fatal(err)
	}
	for _, l := range b.Links.In {
		l := l
		b.HandleIn(l, func(in server.Inbound) (server.Reply, error) { return s.handle(in) })
	}

	b.Health.Register("workers", func() health.Check {
		max := b.Config.Int("workers", 32)
		busy := int(s.busy.Load())
		c := health.Check{Status: health.Healthy, Detail: map[string]any{"busy": busy, "max": max, "queued": s.waiting.Load()}}
		switch {
		case busy >= max && s.waiting.Load() > 0:
			c.Status = health.Failing
			c.Detail["cause"] = "pool exhausted, requests queueing"
		case busy*10 >= max*8:
			c.Status = health.Degraded
		}
		return c
	})
	b.Health.Register("behaviour", func() health.Check {
		s.progMu.RLock()
		defer s.progMu.RUnlock()
		return health.Check{Status: health.Healthy, Detail: map[string]any{"handlers": len(s.program.Handlers), "source": s.source}}
	})
	for _, c := range b.Outs() {
		c := c
		b.Health.Register("breaker:"+c.Link.Edge, func() health.Check {
			st := health.Healthy
			up := c.TargetsUp()
			if up < len(c.Link.Targets) {
				st = health.Degraded
			}
			if up == 0 {
				st = health.Failing
			}
			return health.Check{Status: st, Detail: map[string]any{"targets": c.TargetStates()}}
		})
	}

	b.Control.Handle(control.OpBehaviourGet, func(cmd control.Command) (any, error) {
		s.progMu.RLock()
		defer s.progMu.RUnlock()
		return map[string]any{"source": s.source, "program": s.program}, nil
	})
	b.Control.Handle(control.OpBehaviourSet, func(cmd control.Command) (any, error) {
		raw, err := json.Marshal(cmd.Params)
		if err != nil {
			return nil, err
		}
		p, err := behaviour.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", control.ErrBadRequest, err)
		}
		s.progMu.Lock()
		s.program, s.source = p, "control"
		s.progMu.Unlock()
		b.Events.Publish("behaviour.set", map[string]any{"handlers": len(p.Handlers)})
		return map[string]any{"handlers": len(p.Handlers)}, nil
	})
	b.Control.Handle(control.OpBehaviourReset, func(cmd control.Command) (any, error) {
		s.progMu.Lock()
		s.program, s.source = behaviour.Default(b.Links), "default"
		s.progMu.Unlock()
		return map[string]any{"handlers": len(s.program.Handlers)}, nil
	})
	alias := func(op string, keys ...string) {
		b.Control.Handle(op, func(cmd control.Command) (any, error) {
			patch := map[string]any{}
			for _, k := range keys {
				if v, ok := cmd.Params[k]; ok {
					patch[k] = v
				}
			}
			if len(patch) == 0 {
				return nil, fmt.Errorf("%w: expected one of %v", control.ErrBadRequest, keys)
			}
			if err := b.Config.Set(patch); err != nil {
				return nil, err
			}
			return b.Config.All(), nil
		})
	}
	alias(control.OpPoolSet, "workers", "queue", "queue_timeout_ms")
	alias(control.OpWorkSet, "cpu_ms", "io_ms")
	alias(control.OpBreakerSet, "breaker_threshold", "breaker_open_ms", "timeout_ms", "retry_max", "retry_backoff_ms")
	b.OnReset(func() {
		s.idemMu.Lock()
		s.idem, s.idemQ = map[string]idemEntry{}, nil
		s.idemMu.Unlock()
	})

	b.SetReady(true)
	if err := b.Run(); err != nil {
		log.Fatal(err)
	}
}

func (s *service) loadProgram() error {
	if raw := os.Getenv("SYMPHONY_BEHAVIOUR"); raw != "" {
		p, err := behaviour.Parse([]byte(raw))
		if err != nil {
			return err
		}
		s.program, s.source = p, "env"
		return nil
	}
	s.program, s.source = behaviour.Default(s.b.Links), "default"
	return nil
}

func (s *service) resizePool(n int) {
	if n < 1 {
		n = 1
	}
	s.poolMu.Lock()
	s.pool = make(chan struct{}, n)
	s.poolMu.Unlock()
}

// acquire takes a worker or fails when the queue is full or the wait too
// long. It returns the release for that worker: a resize swaps the pool, and
// a worker goes back to the pool it was taken from.
func (s *service) acquire(ctx context.Context) (release func(), err error) {
	s.poolMu.Lock()
	pool := s.pool
	s.poolMu.Unlock()
	taken := func() func() {
		s.busy.Add(1)
		return func() {
			<-pool
			s.busy.Add(-1)
		}
	}
	if int(s.waiting.Load()) >= s.b.Config.Int("queue", 64) {
		return nil, &server.Reject{Status: http.StatusServiceUnavailable, Msg: "overloaded: queue full", Cause: "overload"}
	}
	select {
	case pool <- struct{}{}:
		return taken(), nil
	default:
	}
	s.waiting.Add(1)
	defer s.waiting.Add(-1)
	wait := s.b.Config.Duration("queue_timeout_ms", 2*time.Second)
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case pool <- struct{}{}:
		return taken(), nil
	case <-t.C:
		return nil, &server.Reject{Status: http.StatusServiceUnavailable, Msg: "overloaded: no worker within queue_timeout_ms", Cause: "overload"}
	case <-ctx.Done():
		return nil, &server.Reject{Status: http.StatusGatewayTimeout, Msg: "caller gave up while queued", Cause: "deadline"}
	}
}

func (s *service) handle(in server.Inbound) (server.Reply, error) {
	release, err := s.acquire(in.Ctx)
	if err != nil {
		return server.Reply{}, err
	}
	defer release()

	// Idempotency: a retry of the same saga step (the caller timed out, the
	// network duplicated it) gets the original answer, not a second execution.
	key := in.Request.Saga + "|" + in.Link.Edge + "|" + in.Request.Step
	if prev, ok := s.seen(key); ok {
		s.dupes.Inc()
		s.b.Log.Debug("duplicate request replayed", "saga", in.Request.Saga, "edge", in.Link.Edge)
		return prev.reply, prev.err
	}
	reply, err := s.run(in)
	s.remember(key, reply, err)
	return reply, err
}

func (s *service) run(in server.Inbound) (server.Reply, error) {
	s.progMu.RLock()
	prog := s.program
	s.progMu.RUnlock()
	h, ok := prog.Match(in.Link)
	if !ok {
		return server.Reply{}, &server.Reject{Status: http.StatusNotImplemented, Msg: "no behaviour handler for edge " + in.Link.Edge, Cause: "behaviour"}
	}
	res := behaviour.Run(in.Ctx, s, h, behaviour.Input{Link: in.Link, Meta: in.Meta, Request: in.Request})
	body, _ := json.Marshal(res.Body)
	if res.Err != nil {
		return server.Reply{}, &server.Reject{Status: res.Status, Msg: res.Err.Error(), Cause: "handler", Outcome: res.Outcome}
	}
	if res.Status >= 400 {
		return server.Reply{}, &server.Reject{Status: res.Status, Msg: "handler replied " + fmt.Sprint(res.Status), Cause: "handler", Outcome: res.Outcome}
	}
	return server.Reply{Result: body, Outcome: res.Outcome}, nil
}

type idemEntry struct {
	reply server.Reply
	err   error
}

// seen looks a saga/edge/step key up in the idempotency store.
func (s *service) seen(key string) (idemEntry, bool) {
	s.idemMu.Lock()
	defer s.idemMu.Unlock()
	e, ok := s.idem[key]
	return e, ok
}

// remember stores the answer for a key (bounded, oldest evicted first).
func (s *service) remember(key string, reply server.Reply, err error) {
	s.idemMu.Lock()
	defer s.idemMu.Unlock()
	if _, ok := s.idem[key]; ok {
		return
	}
	s.idem[key] = idemEntry{reply: reply, err: err}
	s.idemQ = append(s.idemQ, key)
	if len(s.idemQ) > 20000 {
		delete(s.idem, s.idemQ[0])
		s.idemQ = s.idemQ[1:]
	}
}

// --- behaviour.Env ----------------------------------------------------------

func (s *service) Node() string { return s.b.Node }

func (s *service) Rand() float64 { return s.b.Rand() }

func (s *service) ConfigString(key string) string { return s.b.Config.String(key, "") }

func (s *service) Send(ctx context.Context, edge string, meta wire.Meta, req wire.Request, timeout time.Duration) (wire.Response, error) {
	c, ok := s.b.Out(edge)
	if !ok {
		return wire.Response{}, fmt.Errorf("no outbound link %q", edge)
	}
	if timeout > 0 {
		if d := time.Now().Add(timeout); meta.Deadline.IsZero() || d.Before(meta.Deadline) {
			meta.Deadline = d
		}
	}
	meta.Step = req.Step
	res := c.Call(ctx, meta, req)
	if res.Err != nil {
		return res.Response, res.Err
	}
	return res.Response, nil
}

func (s *service) Work(ctx context.Context, cpuMs, ioMs float64) error {
	if cpuMs < 0 {
		cpuMs = s.b.Config.Float("cpu_ms", 2)
	}
	if ioMs < 0 {
		ioMs = s.b.Config.Float("io_ms", 0)
	}
	if cpuMs > 0 {
		until := time.Now().Add(time.Duration(cpuMs * float64(time.Millisecond)))
		x := uint64(1)
		for time.Now().Before(until) {
			x = x*6364136223846793005 + 1442695040888963407
		}
		_ = x
	}
	if ioMs > 0 {
		select {
		case <-time.After(time.Duration(ioMs * float64(time.Millisecond))):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// ReportOutcome posts saga.done to the gateway's admin port.
func (s *service) ReportOutcome(saga, outcome string) {
	gw := s.b.Links.Gateway
	if gw == "" {
		return
	}
	body, _ := json.Marshal(control.SagaDone(saga, outcome, s.b.Node))
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, gw+"/control", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if s.b.Token != "" {
			req.Header.Set("Authorization", "Bearer "+s.b.Token)
		}
		resp, err := s.b.HTTP.Do(req)
		if err != nil {
			s.b.Log.Warn("saga.done not delivered", "saga", saga, "err", err)
			s.b.Metrics.Counter("outcome_reports_total", metrics.Labels{"result": "error"}).Inc()
			return
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			s.b.Metrics.Counter("outcome_reports_total", metrics.Labels{"result": "rejected"}).Inc()
			return
		}
		s.b.Metrics.Counter("outcome_reports_total", metrics.Labels{"result": "ok"}).Inc()
	}()
}

var _ behaviour.Env = (*service)(nil)
