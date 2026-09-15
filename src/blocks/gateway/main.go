// Command gateway is the ingress and the load generator of a topology. It
// mints sagas, sends them down its outbound edges, tracks every saga to its
// outcome (from the reply, or reported later by a sink through saga.done)
// and is where end-to-end latency and throughput are measured.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"symphony/blocks/common/control"
	"symphony/blocks/common/health"
	"symphony/blocks/common/metrics"
	"symphony/blocks/common/server"
	"symphony/blocks/common/wire"
)

type saga struct {
	ID      string     `json:"id"`
	Start   time.Time  `json:"-"`
	StartMs int64      `json:"start"`
	Done    bool       `json:"done"`
	Outcome string     `json:"outcome"`
	LatMs   float64    `json:"latency_ms"`
	Trace   []wire.Hop `json:"trace,omitempty"`
	Source  string     `json:"source"` // load | ingress
}

type loadSpec struct {
	Pattern      string  `json:"pattern"` // constant | ramp | burst | sine | step
	RPS          float64 `json:"rps"`
	ToRPS        float64 `json:"to_rps,omitempty"`    // ramp target
	Factor       float64 `json:"factor,omitempty"`    // burst multiplier
	BurstMs      int     `json:"burst_ms,omitempty"`  // burst length
	PeriodMs     int     `json:"period_ms,omitempty"` // burst / sine period
	StepEveryMs  int     `json:"step_every_ms,omitempty"`
	StepRPS      float64 `json:"step_rps,omitempty"`
	DurationMs   int     `json:"duration_ms,omitempty"` // 0 = until load.stop
	Concurrency  int     `json:"concurrency,omitempty"`
	PayloadBytes int     `json:"payload_bytes,omitempty"`
}

type gateway struct {
	b *server.Block

	mu      sync.Mutex
	sagas   map[string]*saga
	recent  []string
	seq     atomic.Int64
	rr      uint32
	pending atomic.Int64

	loadMu    sync.Mutex
	load      *loadSpec
	loadStart time.Time
	loadStop  context.CancelFunc
	sent      atomic.Int64
	achieved  atomic.Int64 // sent in the last second window
	achievedT atomic.Int64

	bucketMu   sync.Mutex
	tokens     float64
	lastRefill time.Time

	sagaTotal   func(outcome string) *metrics.Counter
	sagaLatency *metrics.Histogram
	inflight    *metrics.Counter
}

const recentCap = 200

func main() {
	b, err := server.New("infra")
	if err != nil {
		log.Fatal(err)
	}
	g := &gateway{b: b, sagas: map[string]*saga{}, lastRefill: time.Now()}
	b.Config.Define("rate_limit_rps", 0.0)
	b.Config.Define("saga_timeout_ms", 10000.0)
	// the gateway waits for a saga as long as the saga is allowed to take,
	// not the generic per-call timeout
	b.Config.Override("timeout_ms", b.Config.Float("saga_timeout_ms", 10000))
	b.Config.Watch(func(key string, v any) {
		if key == "saga_timeout_ms" {
			_ = b.Config.Set(map[string]any{"timeout_ms": b.Config.Float("saga_timeout_ms", 10000)})
		}
	})
	// a saga is attempted once: the orchestrator or the sink decides its
	// fate, and a gateway retry would only hide failures from the numbers
	b.Config.Override("retry_max", 0.0)
	// no breaker either: a load generator keeps sending so the failure rate
	// is measured, it does not protect the system from itself
	b.Config.Override("breaker_threshold", 0.0)
	b.Config.Define("health.max_pending", 500.0)
	g.sagaTotal = func(outcome string) *metrics.Counter {
		return b.Metrics.Counter("saga_total", metrics.Labels{"outcome": outcome})
	}
	g.sagaLatency = b.Metrics.Histogram("saga_latency_ms", nil)
	g.inflight = b.Metrics.Counter("saga_inflight", nil)
	for _, o := range []string{"ok", "failed", "compensated", "timeout", "rejected"} {
		g.sagaTotal(o) // pre-create so the scrape sees zeroes
	}

	b.Data.HandleFunc("POST /ingress", g.ingress)
	b.Data.HandleFunc("GET /ingress", g.ingress)
	for _, l := range b.Links.In {
		l := l
		// a gateway can also be called (a chain of gateways); it forwards
		b.HandleIn(l, func(in server.Inbound) (server.Reply, error) {
			out, err := g.forward(in.Ctx, in.Meta, in.Request, "ingress")
			if err != nil {
				return server.Reply{}, err
			}
			return server.Reply{Result: out}, nil
		})
	}

	b.Control.Handle("load.set", g.loadSet)
	b.Control.Handle("load.stop", func(cmd control.Command) (any, error) {
		g.stopLoad()
		return g.loadStatus(), nil
	})
	b.Control.Handle("load.status", func(cmd control.Command) (any, error) { return g.loadStatus(), nil })
	b.Control.Handle("saga.done", g.sagaDone)
	b.Control.Handle("sagas.recent", func(cmd control.Command) (any, error) {
		n := cmd.Int("n", 20)
		g.mu.Lock()
		defer g.mu.Unlock()
		out := []saga{}
		for i := len(g.recent) - 1; i >= 0 && len(out) < n; i-- {
			if s := g.sagas[g.recent[i]]; s != nil {
				out = append(out, *s)
			}
		}
		return out, nil
	})
	b.Control.Handle("sagas.summary", func(cmd control.Command) (any, error) { return g.summary(), nil })

	b.Health.Register("generator", func() health.Check {
		st := g.loadStatus()
		c := health.Check{Status: health.Healthy, Detail: st}
		if run, _ := st["running"].(bool); run {
			target, _ := st["target_rps"].(float64)
			ach, _ := st["achieved_rps"].(float64)
			if target > 0 && ach < target*0.8 && time.Since(g.loadStart) > 3*time.Second {
				c.Status = health.Degraded
				c.Detail["cause"] = "cannot reach target rps (concurrency cap or slow upstream)"
			}
		}
		return c
	})
	b.Health.Register("upstream", func() health.Check {
		up, total := 0, 0
		for _, c := range b.Outs() {
			if c.Link.Kind != "calls" {
				continue
			}
			total++
			if c.TargetsUp() > 0 {
				up++
			}
		}
		c := health.Check{Status: health.Healthy, Detail: map[string]any{"edges_up": up, "edges": total}}
		if total == 0 {
			c.Status = health.Degraded
			c.Detail["cause"] = "no outbound calls edge; nowhere to send traffic"
		} else if up == 0 {
			c.Status = health.Failing
		} else if up < total {
			c.Status = health.Degraded
		}
		return c
	})
	b.Health.Register("inflight", func() health.Check {
		p := g.pending.Load()
		c := health.Check{Status: health.Healthy, Detail: map[string]any{"pending": p}}
		if float64(p) > b.Config.Float("health.max_pending", 500) {
			c.Status = health.Degraded
			c.Detail["cause"] = "too many sagas waiting for an outcome"
		}
		return c
	})
	b.OnReset(func() {
		g.stopLoad()
		g.mu.Lock()
		g.sagas, g.recent = map[string]*saga{}, nil
		g.mu.Unlock()
		g.pending.Store(0)
		g.inflight.Set(0)
	})

	go g.expireLoop()
	b.SetReady(true)
	if err := b.Run(); err != nil {
		log.Fatal(err)
	}
}

// --- sagas -----------------------------------------------------------------

func (g *gateway) newSaga(source string) *saga {
	id := fmt.Sprintf("%s-%d-%d", g.b.Node, time.Now().UnixMilli()%1000000, g.seq.Add(1))
	s := &saga{ID: id, Start: time.Now(), StartMs: time.Now().UnixMilli(), Source: source}
	g.mu.Lock()
	g.sagas[id] = s
	g.recent = append(g.recent, id)
	if len(g.recent) > recentCap {
		old := g.recent[0]
		g.recent = g.recent[1:]
		if os := g.sagas[old]; os != nil && os.Done {
			delete(g.sagas, old)
		}
	}
	g.mu.Unlock()
	g.pending.Add(1)
	g.inflight.Set(g.pending.Load())
	return s
}

func (g *gateway) finish(s *saga, outcome string, trace []wire.Hop) {
	g.mu.Lock()
	if s.Done {
		g.mu.Unlock()
		return
	}
	s.Done = true
	s.Outcome = outcome
	s.LatMs = float64(time.Since(s.Start).Microseconds()) / 1000
	if trace != nil {
		s.Trace = trace
	}
	g.mu.Unlock()
	g.pending.Add(-1)
	g.inflight.Set(g.pending.Load())
	g.sagaTotal(outcome).Inc()
	g.sagaLatency.Observe(s.LatMs)
}

// expireLoop times out sagas whose outcome never arrived.
func (g *gateway) expireLoop() {
	for range time.Tick(500 * time.Millisecond) {
		limit := g.b.Config.Duration("saga_timeout_ms", 10*time.Second)
		var expired []*saga
		g.mu.Lock()
		for _, s := range g.sagas {
			if !s.Done && time.Since(s.Start) > limit {
				expired = append(expired, s)
			}
		}
		g.mu.Unlock()
		for _, s := range expired {
			g.finish(s, "timeout", nil)
		}
	}
}

// forward sends a request down the next outbound calls edge and interprets
// the reply as the saga's (possibly pending) outcome.
func (g *gateway) forward(ctx context.Context, meta wire.Meta, req wire.Request, source string) (json.RawMessage, error) {
	calls := g.b.Links.OutboundOfKind("calls")
	if len(calls) == 0 {
		return nil, &server.Reject{Status: http.StatusBadGateway, Msg: "gateway has no outbound calls edge", Cause: "links"}
	}
	s := g.newSaga(source)
	req.Saga = s.ID
	meta.Saga = s.ID
	if meta.Deadline.IsZero() {
		meta.Deadline = time.Now().Add(g.b.Config.Duration("saga_timeout_ms", 10*time.Second))
	}
	idx := int(atomic.AddUint32(&g.rr, 1)-1) % len(calls)
	c, _ := g.b.Out(calls[idx].Edge)
	res := c.Call(ctx, meta, req)
	switch {
	case res.Err != nil:
		outcome := "failed"
		if res.Outcome == "timeout" {
			outcome = "timeout"
		}
		if res.Response.Outcome == "compensated" {
			outcome = "compensated"
		}
		g.finish(s, outcome, res.Response.Trace)
		return nil, &server.Reject{Status: http.StatusBadGateway, Msg: res.Err.Error(), Cause: "upstream"}
	case res.Response.Outcome == "pending":
		// the saga continues through a broker; a sink will report saga.done
	default:
		o := res.Response.Outcome
		if o == "" {
			o = "ok"
		}
		g.finish(s, o, res.Response.Trace)
	}
	out, _ := json.Marshal(map[string]any{"saga": s.ID, "outcome": res.Response.Outcome, "upstream": res.Response.Node})
	return out, nil
}

func (g *gateway) ingress(w http.ResponseWriter, r *http.Request) {
	if !g.allow() {
		g.sagaTotal("rejected").Inc()
		http.Error(w, "rate limited", http.StatusTooManyRequests)
		return
	}
	var payload json.RawMessage
	if r.Body != nil {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if json.Valid(raw) {
			payload = raw
		}
	}
	out, err := g.forward(r.Context(), wire.Meta{}, wire.Request{Step: "ingress", Payload: payload}, "ingress")
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		var rj *server.Reject
		status := http.StatusBadGateway
		msg := err.Error()
		if ok := asReject(err, &rj); ok {
			status, msg = rj.Status, rj.Msg
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
		return
	}
	_, _ = w.Write(out)
}

func asReject(err error, rj **server.Reject) bool {
	r, ok := err.(*server.Reject)
	if ok {
		*rj = r
	}
	return ok
}

// allow is the ingress token bucket (rate_limit_rps, 0 = unlimited).
func (g *gateway) allow() bool {
	rps := g.b.Config.Float("rate_limit_rps", 0)
	if rps <= 0 {
		return true
	}
	g.bucketMu.Lock()
	defer g.bucketMu.Unlock()
	now := time.Now()
	g.tokens = math.Min(rps, g.tokens+now.Sub(g.lastRefill).Seconds()*rps)
	g.lastRefill = now
	if g.tokens >= 1 {
		g.tokens--
		return true
	}
	return false
}

func (g *gateway) sagaDone(cmd control.Command) (any, error) {
	id := cmd.String("saga", "")
	outcome := cmd.String("outcome", "ok")
	if id == "" {
		return nil, fmt.Errorf("%w: params.saga required", control.ErrBadRequest)
	}
	g.mu.Lock()
	s := g.sagas[id]
	g.mu.Unlock()
	if s == nil {
		g.b.Metrics.Counter("saga_done_unknown_total", nil).Inc()
		return map[string]any{"known": false}, nil
	}
	g.finish(s, outcome, nil)
	return map[string]any{"known": true, "latency_ms": s.LatMs}, nil
}

func (g *gateway) summary() map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := map[string]any{"tracked": len(g.sagas), "pending": g.pending.Load()}
	byOutcome := map[string]int{}
	var lats []float64
	for _, s := range g.sagas {
		if s.Done {
			byOutcome[s.Outcome]++
			lats = append(lats, s.LatMs)
		}
	}
	out["by_outcome"] = byOutcome
	if len(lats) > 0 {
		sort.Float64s(lats)
		out["p50_ms"] = lats[len(lats)/2]
		out["p99_ms"] = lats[int(math.Ceil(0.99*float64(len(lats))))-1]
	}
	return out
}

// --- load generator ---------------------------------------------------------

func (g *gateway) loadSet(cmd control.Command) (any, error) {
	raw, _ := json.Marshal(cmd.Params)
	var spec loadSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("%w: %v", control.ErrBadRequest, err)
	}
	if spec.Pattern == "" {
		spec.Pattern = "constant"
	}
	if spec.RPS <= 0 {
		return nil, fmt.Errorf("%w: rps must be > 0", control.ErrBadRequest)
	}
	if spec.Concurrency <= 0 {
		spec.Concurrency = 64
	}
	if spec.PeriodMs <= 0 {
		spec.PeriodMs = 10000
	}
	if spec.Factor <= 0 {
		spec.Factor = 4
	}
	if spec.BurstMs <= 0 {
		spec.BurstMs = 1000
	}
	if spec.DurationMs == 0 && cmd.DurationMs > 0 {
		spec.DurationMs = cmd.DurationMs
	}
	if len(g.b.Links.OutboundOfKind("calls")) == 0 {
		return nil, fmt.Errorf("%w: gateway has no outbound calls edge", control.ErrBadRequest)
	}
	g.stopLoad()
	ctx, cancel := context.WithCancel(context.Background())
	g.loadMu.Lock()
	g.load = &spec
	g.loadStart = time.Now()
	g.loadStop = cancel
	g.loadMu.Unlock()
	g.sent.Store(0)
	g.b.Metrics.Counter("load_target_rps", nil).Set(int64(spec.RPS))
	g.b.Events.Publish("load.set", spec)
	go g.generate(ctx, spec)
	return g.loadStatus(), nil
}

func (g *gateway) stopLoad() {
	g.loadMu.Lock()
	if g.loadStop != nil {
		g.loadStop()
		g.loadStop = nil
		g.b.Events.Publish("load.stop", nil)
	}
	g.load = nil
	g.loadMu.Unlock()
	g.b.Metrics.Counter("load_target_rps", nil).Set(0)
}

// rate returns the target rps at time t since start for a pattern.
func (spec loadSpec) rate(t time.Duration) float64 {
	ms := float64(t.Milliseconds())
	switch spec.Pattern {
	case "ramp":
		if spec.DurationMs <= 0 {
			return spec.RPS
		}
		f := math.Min(1, ms/float64(spec.DurationMs))
		return spec.RPS + (spec.ToRPS-spec.RPS)*f
	case "burst":
		if math.Mod(ms, float64(spec.PeriodMs)) < float64(spec.BurstMs) {
			return spec.RPS * spec.Factor
		}
		return spec.RPS
	case "sine":
		return math.Max(0.1, spec.RPS*(1+0.5*math.Sin(2*math.Pi*ms/float64(spec.PeriodMs))))
	case "step":
		if spec.StepEveryMs <= 0 {
			return spec.RPS
		}
		return spec.RPS + spec.StepRPS*math.Floor(ms/float64(spec.StepEveryMs))
	}
	return spec.RPS
}

func (g *gateway) generate(ctx context.Context, spec loadSpec) {
	sem := make(chan struct{}, spec.Concurrency)
	payload := make([]byte, 0, spec.PayloadBytes+16)
	if spec.PayloadBytes > 0 {
		payload = append(payload, `{"blob":"`...)
		for i := 0; i < spec.PayloadBytes; i++ {
			payload = append(payload, 'x')
		}
		payload = append(payload, `"}`...)
	} else {
		payload = []byte(`{"order":"synthetic"}`)
	}
	start := time.Now()
	var credit float64
	last := start
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	windowStart := start
	var windowSent int64
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			if spec.DurationMs > 0 && now.Sub(start) > time.Duration(spec.DurationMs)*time.Millisecond {
				g.stopLoad()
				return
			}
			rps := spec.rate(now.Sub(start))
			credit += now.Sub(last).Seconds() * rps
			last = now
			for credit >= 1 {
				credit--
				select {
				case sem <- struct{}{}:
				default:
					g.sagaTotal("rejected").Inc() // concurrency cap: shed
					continue
				}
				g.sent.Add(1)
				windowSent++
				go func() {
					defer func() { <-sem }()
					cctx, cancel := context.WithTimeout(context.Background(), g.b.Config.Duration("saga_timeout_ms", 10*time.Second))
					defer cancel()
					_, _ = g.forward(cctx, wire.Meta{}, wire.Request{Step: "ingress", Payload: payload}, "load")
				}()
			}
			if now.Sub(windowStart) >= time.Second {
				g.achieved.Store(int64(float64(windowSent) / now.Sub(windowStart).Seconds()))
				g.achievedT.Store(now.UnixMilli())
				windowStart, windowSent = now, 0
			}
		}
	}
}

func (g *gateway) loadStatus() map[string]any {
	g.loadMu.Lock()
	defer g.loadMu.Unlock()
	st := map[string]any{"running": g.load != nil, "sent": g.sent.Load(), "pending": g.pending.Load()}
	if g.load != nil {
		st["spec"] = *g.load
		st["elapsed_ms"] = time.Since(g.loadStart).Milliseconds()
		st["target_rps"] = g.load.rate(time.Since(g.loadStart))
		ach := float64(g.achieved.Load())
		if time.Now().UnixMilli()-g.achievedT.Load() > 2000 {
			ach = 0
		}
		st["achieved_rps"] = ach
	}
	return st
}

var _ = strconv.Itoa
