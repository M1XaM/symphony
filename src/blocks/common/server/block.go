// Package server assembles a block: identity from the environment, the link
// registry, config, faults, metrics, health, control and the two HTTP servers
// (data on 8080, admin on 9090). Blocks build on it and add their own
// handlers, checks and control operations.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"symphony/blocks/common/chaos"
	"symphony/blocks/common/client"
	"symphony/blocks/common/control"
	"symphony/blocks/common/faults"
	"symphony/blocks/common/health"
	"symphony/blocks/common/links"
	"symphony/blocks/common/metrics"
	"symphony/blocks/common/wire"
)

// Version of the blocks contract; reported in /health.
const Version = "v0.1.0"

// Default ports; SYMPHONY_DATA_PORT / SYMPHONY_ADMIN_PORT override them so
// several blocks can share one host (tests, running blocks by hand).
const (
	DefaultDataPort  = 8080
	DefaultAdminPort = 9090
)

func dataPort() int  { return envInt("SYMPHONY_DATA_PORT", DefaultDataPort) }
func adminPort() int { return envInt("SYMPHONY_ADMIN_PORT", DefaultAdminPort) }

// Block is everything a running block has.
type Block struct {
	Node, Kind, Deployment string
	Replica, Replicas      int
	Token                  string

	Links   links.Set
	Config  *control.Config
	Events  *control.Events
	Control *control.Router
	Faults  *faults.Store
	Metrics *metrics.Registry
	Health  *health.Registry
	Logs    *LogBuffer
	Log     *slog.Logger
	Gate    *chaos.Gate
	Memory  *chaos.Memory
	HTTP    *http.Client

	Data  *http.ServeMux
	Admin *http.ServeMux

	rngMu sync.Mutex
	rng   *rand.Rand
	outs  map[string]*client.Client
	// InboundHandled counts requests per inbound edge for the "unknown link" check.
	unknownLinks *metrics.Counter
	ready        bool
	readyMu      sync.Mutex
}

// New reads the environment and builds a block of the given kind. Flags:
// -healthcheck makes the binary probe its own admin port and exit (used by
// the container HEALTHCHECK, distroless images have no curl).
func New(kind string) (*Block, error) {
	hc := flag.Bool("healthcheck", false, "probe the local admin port and exit")
	flag.Parse()
	if *hc {
		os.Exit(selfCheck())
	}

	b := &Block{
		Kind:       kind,
		Node:       envOr("SYMPHONY_NODE", kind),
		Deployment: envOr("SYMPHONY_DEPLOYMENT", "local"),
		Token:      os.Getenv("SYMPHONY_ADMIN_TOKEN"),
		Replica:    envInt("SYMPHONY_REPLICA", 1),
		Replicas:   envInt("SYMPHONY_REPLICAS", 1),
		rng:        rand.New(rand.NewSource(time.Now().UnixNano())),
		outs:       map[string]*client.Client{},
		Data:       http.NewServeMux(),
		Admin:      http.NewServeMux(),
		Gate:       chaos.NewGate(),
		Memory:     &chaos.Memory{},
		HTTP:       client.DefaultTransport(),
	}
	if k := os.Getenv("SYMPHONY_KIND"); k != "" && k != kind {
		return nil, fmt.Errorf("this image is a %s block, but SYMPHONY_KIND=%s", kind, k)
	}
	b.Logs = NewLogBuffer()
	b.Log = slog.New(b.Logs).With("node", b.Node, "kind", kind, "replica", b.Replica)
	slog.SetDefault(b.Log)

	var err error
	if b.Links, err = links.FromEnv(); err != nil {
		return nil, err
	}
	b.Config = control.NewConfig()
	b.Config.Define("log_level", "info")
	b.Config.Define("timeout_ms", 800.0)
	b.Config.Define("retry_max", 3.0)
	b.Config.Define("retry_backoff_ms", 50.0)
	b.Config.Define("breaker_threshold", 5.0)
	b.Config.Define("breaker_open_ms", 5000.0)
	b.Config.Define("health.link_warn_rate", 0.05)
	b.Config.Define("health.link_fail_rate", 0.5)
	if err := b.Config.LoadEnv(); err != nil {
		return nil, err
	}
	b.Events = control.NewEvents()
	b.Control = control.NewRouter(b.Config, b.Events)
	b.Faults = b.Control.Faults
	b.Metrics = metrics.NewRegistry()
	b.Health = health.NewRegistry(b.Node, kind, Version, b.Replica)
	b.unknownLinks = b.Metrics.Counter("unknown_link_requests_total", nil)
	_ = b.Logs.SetLevel(b.Config.String("log_level", "info"))
	b.Config.Watch(func(key string, v any) {
		if key == "log_level" {
			_ = b.Logs.SetLevel(fmt.Sprint(v))
		}
	})

	for _, l := range b.Links.Out {
		b.outs[l.Edge] = client.New(b.Node, l, b.settings, b.Faults, b.Metrics, b.HTTP)
	}
	b.registerCommon()
	return b, nil
}

func (b *Block) settings() client.Settings {
	return client.Settings{
		Timeout:          b.Config.Duration("timeout_ms", 800*time.Millisecond),
		Retries:          b.Config.Int("retry_max", 3),
		BackoffBase:      b.Config.Duration("retry_backoff_ms", 50*time.Millisecond),
		BreakerThreshold: b.Config.Int("breaker_threshold", 5),
		BreakerOpen:      b.Config.Duration("breaker_open_ms", 5*time.Second),
	}
}

// Out returns the client for an outbound edge.
func (b *Block) Out(edge string) (*client.Client, bool) {
	c, ok := b.outs[edge]
	return c, ok
}

// ReplaceOut swaps the client of an outbound edge (route.set).
func (b *Block) ReplaceOut(l links.Link) error {
	if len(l.Targets) == 0 {
		return errors.New("no targets")
	}
	for i := range b.Links.Out {
		if b.Links.Out[i].Edge == l.Edge {
			b.Links.Out[i] = l
		}
	}
	b.outs[l.Edge] = client.New(b.Node, l, b.settings, b.Faults, b.Metrics, b.HTTP)
	return nil
}

// Outs returns every outbound client, in link order.
func (b *Block) Outs() []*client.Client {
	out := make([]*client.Client, 0, len(b.Links.Out))
	for _, l := range b.Links.Out {
		out = append(out, b.outs[l.Edge])
	}
	return out
}

// Rand is a locked random source.
func (b *Block) Rand() float64 {
	b.rngMu.Lock()
	defer b.rngMu.Unlock()
	return b.rng.Float64()
}

// SetReady flips the readiness check (blocks call it once their handlers
// are installed).
func (b *Block) SetReady(ready bool) {
	b.readyMu.Lock()
	b.ready = ready
	b.readyMu.Unlock()
}

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
			if cl, ok := b.outs[edge]; ok {
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

// registerCommon adds the checks and control ops every block has.
func (b *Block) registerCommon() {
	b.Health.Register("ready", func() health.Check {
		b.readyMu.Lock()
		defer b.readyMu.Unlock()
		if !b.ready {
			return health.Check{Status: health.Failing, Detail: map[string]any{"reason": "handlers not installed yet"}}
		}
		return health.Check{Status: health.Healthy}
	})
	b.Health.Register("faults", func() health.Check {
		list := b.Faults.List()
		c := health.Check{Status: health.Healthy, Detail: map[string]any{"active": len(list)}}
		if len(list) > 0 {
			c.Status = health.Degraded
			items := make([]map[string]any, 0, len(list))
			for _, f := range list {
				items = append(items, map[string]any{"id": f.ID, "kind": f.Kind, "target": f.Target, "params": f.Params, "remaining_ms": f.RemainingMs()})
				if f.Kind == faults.Partition || f.Kind == faults.Timeout {
					c.Status = health.Worst(c.Status, health.Failing)
				}
			}
			c.Detail["items"] = items
		}
		return c
	})
	b.Health.Register("chaos", func() health.Check {
		c := health.Check{Status: health.Healthy, Detail: map[string]any{"held_mb": b.Memory.HeldMB()}}
		if s := b.Gate.Stalled(); s > 0 {
			c.Status = health.Failing
			c.Detail["stalled_ms"] = s.Milliseconds()
		}
		return c
	})
	b.Health.Register("memory", func() health.Check {
		ms := readMem()
		return health.Check{Status: health.Healthy, Detail: ms}
	})
	b.Health.Register("links", func() health.Check {
		unknown := b.unknownLinks.Value()
		c := health.Check{Status: health.Healthy, Detail: map[string]any{"in": len(b.Links.In), "out": len(b.Links.Out), "unknown_requests": unknown}}
		if unknown > 0 {
			c.Status = health.Degraded
			c.Detail["cause"] = "requests arrived for edges this node does not have"
		}
		return c
	})
	for _, l := range b.Links.Out {
		b.Health.Register("link:"+l.Edge+":out", b.linkCheck(l.Edge, "out"))
	}

	b.Control.Handle("chaos.cpu", func(cmd control.Command) (any, error) {
		cores := cmd.Float("cores", 1)
		d := cmd.Duration()
		if d <= 0 {
			d = 10 * time.Second
		}
		chaos.Burn(context.Background(), cores, d)
		b.Events.Publish("chaos.cpu", cmd.Params)
		return map[string]any{"cores": cores, "duration_ms": d.Milliseconds()}, nil
	})
	b.Control.Handle("chaos.memory", func(cmd control.Command) (any, error) {
		mb := cmd.Int("mb", 100)
		b.Memory.Hold(mb, cmd.Duration())
		b.Events.Publish("chaos.memory", cmd.Params)
		return map[string]any{"held_mb": b.Memory.HeldMB()}, nil
	})
	b.Control.Handle("chaos.stall", func(cmd control.Command) (any, error) {
		d := cmd.Duration()
		if d <= 0 {
			d = 5 * time.Second
		}
		b.Gate.Stall(d)
		b.Events.Publish("chaos.stall", map[string]any{"duration_ms": d.Milliseconds()})
		return map[string]any{"stalled_ms": d.Milliseconds()}, nil
	})
	b.Control.Handle("chaos.resume", func(cmd control.Command) (any, error) {
		b.Gate.Open()
		return map[string]any{"stalled_ms": 0}, nil
	})
	b.Control.Handle("chaos.crash", func(cmd control.Command) (any, error) {
		code := cmd.Int("exit_code", 137)
		b.Log.Error("crashing on request", "exit_code", code)
		chaos.Crash(code)
		return map[string]any{"exit_code": code}, nil
	})
	b.Control.Handle("log.level", func(cmd control.Command) (any, error) {
		lv := cmd.String("level", "")
		if err := b.Logs.SetLevel(lv); err != nil {
			return nil, fmt.Errorf("%w: %v", control.ErrBadRequest, err)
		}
		return map[string]any{"level": b.Logs.Level()}, nil
	})
	b.Control.Handle("links.get", func(cmd control.Command) (any, error) { return b.Links, nil })
	b.Control.Handle("targets.get", func(cmd control.Command) (any, error) {
		out := map[string]any{}
		for edge, c := range b.outs {
			out[edge] = c.TargetStates()
		}
		return out, nil
	})
	prevReset := b.Control.Reset
	b.Control.Reset = func() {
		b.Metrics.Reset()
		b.Memory.Release(1 << 30)
		b.Gate.Open()
		if prevReset != nil {
			prevReset()
		}
	}
}

// OnReset chains a block-specific reset behind the common one.
func (b *Block) OnReset(fn func()) {
	prev := b.Control.Reset
	b.Control.Reset = func() {
		if prev != nil {
			prev()
		}
		fn()
	}
}

// Run installs the admin routes and serves both ports until SIGTERM.
func (b *Block) Run() error {
	b.Admin.HandleFunc("GET /health", b.Health.Handler())
	b.Admin.HandleFunc("POST /control", b.Control.HTTPHandler())
	b.Admin.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") == "prometheus" {
			w.Header().Set("Content-Type", "text/plain; version=0.0.4")
			_, _ = io.WriteString(w, b.Metrics.Prometheus())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(b.Metrics.Snapshot(time.Now().UnixMilli()))
	})
	b.Admin.HandleFunc("GET /logs", b.Logs.Handler())
	b.Admin.HandleFunc("GET /events", b.Events.Handler())
	b.Admin.HandleFunc("GET /snapshot", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"node": b.Node, "kind": b.Kind, "deployment": b.Deployment, "replica": b.Replica,
			"links": b.Links, "config": b.Config.All(), "faults": b.Faults.List(),
			"health": b.Health.Document(), "metrics": b.Metrics.Snapshot(time.Now().UnixMilli()),
		})
	})
	b.Data.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/in/") {
			b.unknownLinks.Inc()
			http.Error(w, "unknown link", http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, "symphony %s block %q (%s)\n", b.Kind, b.Node, Version)
	})

	data := &http.Server{Addr: ":" + strconv.Itoa(dataPort()), Handler: b.Data, ReadHeaderTimeout: 5 * time.Second}
	admin := &http.Server{Addr: ":" + strconv.Itoa(adminPort()), Handler: b.auth(b.Admin), ReadHeaderTimeout: 5 * time.Second}

	errc := make(chan error, 2)
	go func() { errc <- serve(data) }()
	go func() { errc <- serve(admin) }()
	b.Log.Info("block up", "deployment", b.Deployment, "data_port", dataPort(), "admin_port", adminPort(), "in", len(b.Links.In), "out", len(b.Links.Out))

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	select {
	case err := <-errc:
		return err
	case s := <-sig:
		b.Log.Info("shutting down", "signal", s.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = data.Shutdown(ctx)
	_ = admin.Shutdown(ctx)
	return nil
}

func serve(s *http.Server) error {
	if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// auth requires the bearer token on the admin port when one is configured.
func (b *Block) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b.Token != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(b.Token)) != 1 {
				// the healthcheck runs inside the container and knows the token too
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// selfCheck is the container HEALTHCHECK: strict health on the admin port.
func selfCheck() int {
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/health?strict=1", adminPort()), nil)
	if tok := os.Getenv("SYMPHONY_ADMIN_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	c := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{DialContext: (&net.Dialer{Timeout: time.Second}).DialContext}}
	resp, err := c.Do(req)
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return 0
	}
	return 1
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}
