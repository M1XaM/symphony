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
const Version = wire.Contract

// Default ports; SYMPHONY_DATA_PORT / SYMPHONY_ADMIN_PORT override them so
// several blocks can share one host (tests, running blocks by hand).
const (
	DefaultDataPort  = 8080
	DefaultAdminPort = 9090
)

func dataPort() int { return envInt("SYMPHONY_DATA_PORT", DefaultDataPort) }

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
	// one client per outbound edge; route.set replaces one while requests
	// are being served, hence the lock
	outsMu sync.RWMutex
	outs   map[string]*client.Client
	// InboundHandled counts requests per inbound edge for the "unknown link" check.
	unknownLinks *metrics.Counter
	ready        bool
	readyMu      sync.Mutex
	// the context CPU burns run under; chaos.clear cancels it
	burnMu   sync.Mutex
	burnCtx  context.Context
	burnStop context.CancelFunc
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
	b.outsMu.RLock()
	defer b.outsMu.RUnlock()
	c, ok := b.outs[edge]
	return c, ok
}

// ReplaceOut swaps the client of an outbound edge for one with other
// targets (route.set). Links keeps what the engine wired at the start;
// targets.get says where each edge goes now.
func (b *Block) ReplaceOut(l links.Link) error {
	if len(l.Targets) == 0 {
		return errors.New("no targets")
	}
	c := client.New(b.Node, l, b.settings, b.Faults, b.Metrics, b.HTTP)
	b.outsMu.Lock()
	b.outs[l.Edge] = c
	b.outsMu.Unlock()
	return nil
}

// Outs returns every outbound client, in link order.
func (b *Block) Outs() []*client.Client {
	b.outsMu.RLock()
	defer b.outsMu.RUnlock()
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
		// ?window_ms= is how far back the percentiles look
		window, _ := strconv.Atoi(r.URL.Query().Get("window_ms"))
		_ = json.NewEncoder(w).Encode(b.Metrics.SnapshotWindow(time.Now().UnixMilli(), time.Duration(window)*time.Millisecond))
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
