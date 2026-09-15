// Command database is an in-memory store that behaves like a database under
// pressure: a bounded connection pool with a wait timeout, read/write/fsync
// latency, per-key locks with transactions, and a storage cap.
package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"symphony/blocks/common/control"
	"symphony/blocks/common/health"
	"symphony/blocks/common/metrics"
	"symphony/blocks/common/server"
)

const stripes = 256

type op struct {
	Op     string `json:"op"`
	Key    string `json:"key,omitempty"`
	Value  string `json:"value,omitempty"`
	Prefix string `json:"prefix,omitempty"`
	Ops    []op   `json:"ops,omitempty"`
}

type database struct {
	b *server.Block

	storeMu sync.RWMutex
	store   map[string]string
	locks   [stripes]sync.Mutex
	waiting atomic.Int64

	poolMu   sync.Mutex
	pool     chan struct{}
	poolBusy atomic.Int64
	poolWait atomic.Int64

	slow    atomic.Int64
	opTotal func(op, result string) *metrics.Counter
	opLat   func(op string) *metrics.Histogram
	rows    *metrics.Counter
	busy    *metrics.Counter
}

func main() {
	b, err := server.New("db")
	if err != nil {
		log.Fatal(err)
	}
	d := &database{b: b, store: map[string]string{}}
	b.Config.Define("max_connections", 100.0)
	b.Config.Define("statement_timeout_ms", 5000.0)
	b.Config.Define("log_min_duration_ms", 250.0)
	b.Config.Define("read_ms", 1.0)
	b.Config.Define("write_ms", 2.0)
	b.Config.Define("fsync_ms", 3.0)
	b.Config.Define("max_rows", 1000000.0)
	b.Config.Define("hot_key_prefix", "")
	d.resizePool(b.Config.Int("max_connections", 100))
	b.Config.Watch(func(k string, v any) {
		if k == "max_connections" {
			d.resizePool(b.Config.Int("max_connections", 100))
		}
	})
	d.opTotal = func(o, r string) *metrics.Counter {
		return b.Metrics.Counter("db_ops_total", metrics.Labels{"op": o, "result": r})
	}
	d.opLat = func(o string) *metrics.Histogram {
		return b.Metrics.Histogram("db_latency_ms", metrics.Labels{"op": o})
	}
	d.rows = b.Metrics.Counter("db_rows", nil)
	d.busy = b.Metrics.Counter("db_pool_busy", nil)

	for _, l := range b.Links.In {
		l := l
		b.HandleIn(l, func(in server.Inbound) (server.Reply, error) {
			out, err := d.handle(in)
			return server.Reply{Result: out}, err
		})
	}

	b.Health.Register("pool", func() health.Check {
		max := b.Config.Int("max_connections", 100)
		busy := int(d.poolBusy.Load())
		c := health.Check{Status: health.Healthy, Detail: map[string]any{"busy": busy, "max": max, "waiters": d.poolWait.Load()}}
		switch {
		case busy >= max && d.poolWait.Load() > 0:
			c.Status = health.Failing
			c.Detail["cause"] = "connection pool exhausted"
		case busy*10 >= max*8:
			c.Status = health.Degraded
		}
		return c
	})
	b.Health.Register("storage", func() health.Check {
		d.storeMu.RLock()
		n := len(d.store)
		d.storeMu.RUnlock()
		max := b.Config.Int("max_rows", 1000000)
		c := health.Check{Status: health.Healthy, Detail: map[string]any{"rows": n, "max_rows": max}}
		if n >= max {
			c.Status = health.Failing
			c.Detail["cause"] = "store full"
		} else if n*10 >= max*9 {
			c.Status = health.Degraded
		}
		return c
	})
	b.Health.Register("locks", func() health.Check {
		w := d.waiting.Load()
		c := health.Check{Status: health.Healthy, Detail: map[string]any{"waiting": w}}
		if w > 10 {
			c.Status = health.Degraded
			c.Detail["cause"] = "lock contention"
		}
		return c
	})
	b.Health.Register("slow_queries", func() health.Check {
		return health.Check{Status: health.Healthy, Detail: map[string]any{"count": d.slow.Load(), "threshold_ms": b.Config.Float("log_min_duration_ms", 250)}}
	})

	alias := func(opName string, keys ...string) {
		b.Control.Handle(opName, func(cmd control.Command) (any, error) {
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
	alias("pool.set", "max_connections", "statement_timeout_ms")
	alias("latency.set", "read_ms", "write_ms", "fsync_ms")
	alias("store.set", "max_rows", "hot_key_prefix")
	b.Control.Handle("store.fill", func(cmd control.Command) (any, error) {
		n := cmd.Int("rows", 1000)
		d.storeMu.Lock()
		for i := 0; i < n; i++ {
			d.store[fmt.Sprintf("fill/%d/%d", time.Now().UnixNano(), i)] = "x"
		}
		rows := len(d.store)
		d.storeMu.Unlock()
		d.rows.Set(int64(rows))
		return map[string]any{"rows": rows}, nil
	})
	b.Control.Handle("store.get", func(cmd control.Command) (any, error) {
		key := cmd.String("key", "")
		d.storeMu.RLock()
		defer d.storeMu.RUnlock()
		if key != "" {
			v, ok := d.store[key]
			return map[string]any{"key": key, "value": v, "found": ok}, nil
		}
		prefix := cmd.String("prefix", "")
		out := map[string]string{}
		for k, v := range d.store {
			if strings.HasPrefix(k, prefix) && len(out) < 200 {
				out[k] = v
			}
		}
		return out, nil
	})
	b.Control.Handle("store.clear", func(cmd control.Command) (any, error) {
		d.storeMu.Lock()
		d.store = map[string]string{}
		d.storeMu.Unlock()
		d.rows.Set(0)
		return map[string]any{"rows": 0}, nil
	})
	b.OnReset(func() {
		d.storeMu.Lock()
		d.store = map[string]string{}
		d.storeMu.Unlock()
		d.slow.Store(0)
	})

	b.SetReady(true)
	if err := b.Run(); err != nil {
		log.Fatal(err)
	}
}

func (d *database) resizePool(n int) {
	if n < 1 {
		n = 1
	}
	d.poolMu.Lock()
	d.pool = make(chan struct{}, n)
	d.poolMu.Unlock()
}

func (d *database) handle(in server.Inbound) (json.RawMessage, error) {
	var o op
	if len(in.Request.Payload) > 0 {
		if err := json.Unmarshal(in.Request.Payload, &o); err != nil {
			return nil, &server.Reject{Status: http.StatusBadRequest, Msg: "bad op: " + err.Error(), Cause: "envelope"}
		}
	}
	if o.Op == "" {
		o.Op = in.Request.Step
	}
	if o.Op == "" {
		o.Op = "get"
	}
	start := time.Now()
	// connection pool
	d.poolMu.Lock()
	pool := d.pool
	d.poolMu.Unlock()
	d.poolWait.Add(1)
	t := time.NewTimer(d.b.Config.Duration("statement_timeout_ms", 5*time.Second))
	select {
	case pool <- struct{}{}:
		t.Stop()
	case <-t.C:
		d.poolWait.Add(-1)
		d.opTotal(o.Op, "pool_exhausted").Inc()
		return nil, &server.Reject{Status: http.StatusServiceUnavailable, Msg: "too many connections: pool exhausted", Cause: "pool"}
	case <-in.Ctx.Done():
		t.Stop()
		d.poolWait.Add(-1)
		d.opTotal(o.Op, "timeout").Inc()
		return nil, &server.Reject{Status: http.StatusGatewayTimeout, Msg: "caller gave up waiting for a connection", Cause: "deadline"}
	}
	d.poolWait.Add(-1)
	d.poolBusy.Add(1)
	d.busy.Set(d.poolBusy.Load())
	defer func() {
		select {
		case <-pool:
		default:
		}
		d.poolBusy.Add(-1)
		d.busy.Set(d.poolBusy.Load())
	}()

	res, err := d.exec(o)
	ms := float64(time.Since(start).Microseconds()) / 1000
	d.opLat(o.Op).Observe(ms)
	if ms > d.b.Config.Float("log_min_duration_ms", 250) {
		d.slow.Add(1)
		d.b.Log.Warn("slow query", "op", o.Op, "key", o.Key, "ms", ms)
	}
	if err != nil {
		d.opTotal(o.Op, "error").Inc()
		return nil, err
	}
	d.opTotal(o.Op, "ok").Inc()
	return res, nil
}

func (d *database) stripe(key string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return &d.locks[int(h.Sum32())%stripes]
}

func (d *database) exec(o op) (json.RawMessage, error) {
	switch o.Op {
	case "get":
		time.Sleep(d.b.Config.Duration("read_ms", time.Millisecond))
		d.storeMu.RLock()
		v, ok := d.store[o.Key]
		d.storeMu.RUnlock()
		return json.Marshal(map[string]any{"key": o.Key, "value": v, "found": ok})
	case "put", "delete":
		mu := d.stripe(o.Key)
		d.waiting.Add(1)
		mu.Lock()
		d.waiting.Add(-1)
		defer mu.Unlock()
		if hot := d.b.Config.String("hot_key_prefix", ""); hot != "" && strings.HasPrefix(o.Key, hot) {
			time.Sleep(d.b.Config.Duration("write_ms", 2*time.Millisecond) * 5) // contended row
		}
		time.Sleep(d.b.Config.Duration("write_ms", 2*time.Millisecond))
		d.storeMu.Lock()
		if o.Op == "put" {
			if _, exists := d.store[o.Key]; !exists && len(d.store) >= d.b.Config.Int("max_rows", 1000000) {
				d.storeMu.Unlock()
				return nil, &server.Reject{Status: http.StatusInsufficientStorage, Msg: "store full", Cause: "storage"}
			}
			d.store[o.Key] = o.Value
		} else {
			delete(d.store, o.Key)
		}
		n := len(d.store)
		d.storeMu.Unlock()
		d.rows.Set(int64(n))
		time.Sleep(d.b.Config.Duration("fsync_ms", 3*time.Millisecond))
		return json.Marshal(map[string]any{"key": o.Key, "rows": n})
	case "scan":
		time.Sleep(d.b.Config.Duration("read_ms", time.Millisecond))
		d.storeMu.RLock()
		out := map[string]string{}
		for k, v := range d.store {
			if strings.HasPrefix(k, o.Prefix) && len(out) < 100 {
				out[k] = v
			}
		}
		d.storeMu.RUnlock()
		return json.Marshal(out)
	case "txn":
		// lock every key in a stable order, apply, fsync once
		keys := map[string]bool{}
		for _, sub := range o.Ops {
			keys[sub.Key] = true
		}
		var locked []*sync.Mutex
		seen := map[*sync.Mutex]bool{}
		for k := range keys {
			mu := d.stripe(k)
			if !seen[mu] {
				seen[mu] = true
				locked = append(locked, mu)
			}
		}
		d.waiting.Add(1)
		for _, mu := range locked {
			mu.Lock()
		}
		d.waiting.Add(-1)
		defer func() {
			for _, mu := range locked {
				mu.Unlock()
			}
		}()
		time.Sleep(d.b.Config.Duration("write_ms", 2*time.Millisecond) * time.Duration(max(1, len(o.Ops))))
		d.storeMu.Lock()
		for _, sub := range o.Ops {
			switch sub.Op {
			case "put":
				d.store[sub.Key] = sub.Value
			case "delete":
				delete(d.store, sub.Key)
			}
		}
		n := len(d.store)
		d.storeMu.Unlock()
		d.rows.Set(int64(n))
		time.Sleep(d.b.Config.Duration("fsync_ms", 3*time.Millisecond))
		return json.Marshal(map[string]any{"applied": len(o.Ops), "rows": n})
	}
	return nil, &server.Reject{Status: http.StatusBadRequest, Msg: "unknown op " + o.Op, Cause: "envelope"}
}
