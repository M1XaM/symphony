// Command broker is an in-memory message broker: topics with partitions,
// append-only logs with retention, one consumer group per subscribing edge,
// at-least-once push delivery with committed offsets and visible lag.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"symphony/blocks/common/control"
	"symphony/blocks/common/faults"
	"symphony/blocks/common/health"
	"symphony/blocks/common/links"
	"symphony/blocks/common/metrics"
	"symphony/blocks/common/server"
	"symphony/blocks/common/wire"
)

type message struct {
	Offset  int64
	Ts      time.Time
	Saga    string
	Step    string
	Payload json.RawMessage
	Trace   []wire.Hop
}

type partition struct {
	mu   sync.Mutex
	base int64 // offset of msgs[0]
	msgs []message
	head int64 // next offset to assign
}

func (p *partition) append(m message) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	m.Offset = p.head
	p.msgs = append(p.msgs, m)
	p.head++
	return m.Offset
}

// at returns the message at offset if retained.
func (p *partition) at(off int64) (message, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if off < p.base {
		return message{}, false
	}
	i := off - p.base
	if i >= int64(len(p.msgs)) {
		return message{}, false
	}
	return p.msgs[i], true
}

func (p *partition) headOffset() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.head
}

func (p *partition) retain(minTs time.Time) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for n < len(p.msgs) && p.msgs[n].Ts.Before(minTs) {
		n++
	}
	if n > 0 {
		p.msgs = append([]message(nil), p.msgs[n:]...)
		p.base += int64(n)
	}
	return n
}

type topic struct {
	name       string
	partitions []*partition
}

func (t *topic) partitionFor(key string) *partition {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return t.partitions[int(h.Sum32())%len(t.partitions)]
}

// group is the consumer state for one subscribes edge.
type group struct {
	edge      links.Link
	topic     *topic
	committed []int64 // per partition
	paused    bool
	mu        sync.Mutex
	cancel    context.CancelFunc
}

func (g *group) lag() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	var lag int64
	for i, p := range g.topic.partitions {
		lag += p.headOffset() - g.committed[i]
	}
	return lag
}

type broker struct {
	b      *server.Block
	mu     sync.Mutex
	topics map[string]*topic
	groups map[string]*group // edge id -> group

	in       func(topic string) *metrics.Counter
	out      func(topic, group string) *metrics.Counter
	lagGauge func(topic, group string) *metrics.Counter
	redeliv  *metrics.Counter
	lost     *metrics.Counter
}

func main() {
	b, err := server.New("broker")
	if err != nil {
		log.Fatal(err)
	}
	br := &broker{b: b, topics: map[string]*topic{}, groups: map[string]*group{}}
	b.Config.Define("num_partitions", 3.0)
	b.Config.Define("log_retention_ms", 600000.0)
	b.Config.Define("max_poll_records", 500.0)
	b.Config.Define("delivery_delay_ms", 0.0)
	b.Config.Define("health.lag_warn", 100.0)
	b.Config.Define("health.lag_fail", 5000.0)
	b.Config.Define("at_most_once", false)
	br.in = func(t string) *metrics.Counter {
		return b.Metrics.Counter("broker_messages_total", metrics.Labels{"topic": t, "dir": "in"})
	}
	br.out = func(t, g string) *metrics.Counter {
		return b.Metrics.Counter("broker_messages_total", metrics.Labels{"topic": t, "dir": "out", "group": g})
	}
	br.lagGauge = func(t, g string) *metrics.Counter {
		return b.Metrics.Counter("broker_lag", metrics.Labels{"topic": t, "group": g})
	}
	br.redeliv = b.Metrics.Counter("broker_redeliveries_total", nil)
	br.lost = b.Metrics.Counter("broker_lost_total", nil)

	// Every topic named on a link exists from the start.
	for _, l := range append(append([]links.Link{}, b.Links.In...), b.Links.Out...) {
		if l.Topic != "" {
			br.ensureTopic(l.Topic, b.Config.Int("num_partitions", 3))
		}
	}
	for _, l := range b.Links.In {
		l := l
		b.HandleIn(l, func(in server.Inbound) (server.Reply, error) { return br.publish(l, in) })
	}
	for _, l := range b.Links.Out {
		if l.Kind == "subscribes" && l.Topic != "" {
			br.startGroup(l)
		}
	}

	b.Health.Register("topics", func() health.Check {
		br.mu.Lock()
		defer br.mu.Unlock()
		names := make([]string, 0, len(br.topics))
		total := 0
		for n, t := range br.topics {
			names = append(names, n)
			for _, p := range t.partitions {
				p.mu.Lock()
				total += len(p.msgs)
				p.mu.Unlock()
			}
		}
		sort.Strings(names)
		return health.Check{Status: health.Healthy, Detail: map[string]any{"topics": names, "retained_messages": total}}
	})
	for _, l := range b.Links.Out {
		if l.Kind != "subscribes" {
			continue
		}
		edge := l.Edge
		b.Health.Register("lag:"+edge, func() health.Check {
			br.mu.Lock()
			g := br.groups[edge]
			br.mu.Unlock()
			if g == nil {
				return health.Check{Status: health.Failing, Detail: map[string]any{"cause": "no consumer group"}}
			}
			lag := g.lag()
			c := health.Check{Status: health.Healthy, Detail: map[string]any{"lag": lag, "topic": g.topic.name, "paused": g.paused}}
			switch {
			case float64(lag) >= b.Config.Float("health.lag_fail", 5000):
				c.Status = health.Failing
			case float64(lag) >= b.Config.Float("health.lag_warn", 100):
				c.Status = health.Degraded
			}
			if g.paused {
				c.Status = health.Worst(c.Status, health.Degraded)
			}
			return c
		})
	}

	b.Control.Handle("topic.create", func(cmd control.Command) (any, error) {
		name := cmd.String("topic", "")
		if name == "" {
			return nil, fmt.Errorf("%w: params.topic required", control.ErrBadRequest)
		}
		br.ensureTopic(name, cmd.Int("partitions", b.Config.Int("num_partitions", 3)))
		return br.topicsInfo(), nil
	})
	b.Control.Handle("topic.delete", func(cmd control.Command) (any, error) {
		name := cmd.String("topic", "")
		br.mu.Lock()
		delete(br.topics, name)
		br.mu.Unlock()
		return br.topicsInfo(), nil
	})
	b.Control.Handle("topics.get", func(cmd control.Command) (any, error) { return br.topicsInfo(), nil })
	pause := func(paused bool) control.Handler {
		return func(cmd control.Command) (any, error) {
			edge := cmd.String("group", cmd.Target.Edge)
			br.mu.Lock()
			g := br.groups[edge]
			br.mu.Unlock()
			if g == nil {
				return nil, fmt.Errorf("%w: unknown group %q (use the subscribes edge id)", control.ErrBadRequest, edge)
			}
			g.mu.Lock()
			g.paused = paused
			g.mu.Unlock()
			b.Events.Publish("consumer.paused", map[string]any{"group": edge, "paused": paused})
			return map[string]any{"group": edge, "paused": paused, "lag": g.lag()}, nil
		}
	}
	b.Control.Handle("consumer.pause", pause(true))
	b.Control.Handle("consumer.resume", pause(false))
	b.Control.Handle("delivery.set", func(cmd control.Command) (any, error) {
		patch := map[string]any{}
		for _, k := range []string{"delivery_delay_ms", "max_poll_records", "at_most_once"} {
			if v, ok := cmd.Params[k]; ok {
				patch[k] = v
			}
		}
		if err := b.Config.Set(patch); err != nil {
			return nil, err
		}
		return b.Config.All(), nil
	})
	b.OnReset(func() {
		br.mu.Lock()
		for _, t := range br.topics {
			for _, p := range t.partitions {
				p.mu.Lock()
				p.msgs, p.base, p.head = nil, 0, 0
				p.mu.Unlock()
			}
		}
		for _, g := range br.groups {
			g.mu.Lock()
			for i := range g.committed {
				g.committed[i] = 0
			}
			g.paused = false
			g.mu.Unlock()
		}
		br.mu.Unlock()
	})

	go br.retentionLoop()
	b.SetReady(true)
	if err := b.Run(); err != nil {
		log.Fatal(err)
	}
}

func (br *broker) ensureTopic(name string, partitions int) *topic {
	if partitions < 1 {
		partitions = 1
	}
	br.mu.Lock()
	defer br.mu.Unlock()
	if t, ok := br.topics[name]; ok {
		return t
	}
	t := &topic{name: name}
	for i := 0; i < partitions; i++ {
		t.partitions = append(t.partitions, &partition{})
	}
	br.topics[name] = t
	return t
}

func (br *broker) topicsInfo() []map[string]any {
	br.mu.Lock()
	defer br.mu.Unlock()
	out := []map[string]any{}
	for name, t := range br.topics {
		heads := make([]int64, 0, len(t.partitions))
		for _, p := range t.partitions {
			heads = append(heads, p.headOffset())
		}
		groups := map[string]int64{}
		for edge, g := range br.groups {
			if g.topic == t {
				groups[edge] = g.lag()
			}
		}
		out = append(out, map[string]any{"topic": name, "partitions": len(t.partitions), "heads": heads, "groups": groups})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["topic"].(string) < out[j]["topic"].(string) })
	return out
}

// publish appends a message arriving over a publishes edge.
func (br *broker) publish(l links.Link, in server.Inbound) (server.Reply, error) {
	if l.Topic == "" {
		return server.Reply{}, &server.Reject{Status: http.StatusBadRequest, Msg: "edge has no topic", Cause: "links"}
	}
	t := br.ensureTopic(l.Topic, br.b.Config.Int("num_partitions", 3))
	p := t.partitionFor(in.Request.Saga)
	off := p.append(message{Ts: time.Now(), Saga: in.Request.Saga, Step: in.Request.Step, Payload: in.Request.Payload, Trace: in.Request.Trace})
	br.in(l.Topic).Inc()
	out, _ := json.Marshal(map[string]any{"topic": l.Topic, "offset": off})
	return server.Reply{Result: out}, nil
}

func (br *broker) startGroup(l links.Link) {
	t := br.ensureTopic(l.Topic, br.b.Config.Int("num_partitions", 3))
	g := &group{edge: l, topic: t, committed: make([]int64, len(t.partitions))}
	ctx, cancel := context.WithCancel(context.Background())
	g.cancel = cancel
	br.mu.Lock()
	br.groups[l.Edge] = g
	br.mu.Unlock()
	for i := range t.partitions {
		go br.deliverLoop(ctx, g, i)
	}
}

// deliverLoop pushes one partition's messages to the subscriber in order,
// committing after each successful delivery (at-least-once).
func (br *broker) deliverLoop(ctx context.Context, g *group, pi int) {
	p := g.topic.partitions[pi]
	c, ok := br.b.Out(g.edge.Edge)
	if !ok {
		return
	}
	backoff := 50 * time.Millisecond
	for ctx.Err() == nil {
		g.mu.Lock()
		paused := g.paused
		next := g.committed[pi]
		g.mu.Unlock()
		if paused {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		m, have := p.at(next)
		if !have {
			if p.headOffset() > next {
				// retention dropped it: skip forward and count it as lost
				br.lost.Inc()
				g.mu.Lock()
				g.committed[pi] = next + 1
				g.mu.Unlock()
				continue
			}
			time.Sleep(20 * time.Millisecond)
			continue
		}
		// reorder fault: with a window, occasionally deliver the following
		// message first (both still get delivered)
		if fs := br.b.Faults.Matching(faults.Scope{Edge: g.edge.Edge, Direction: "out", Topic: g.topic.name}); len(fs) > 0 {
			for _, f := range fs {
				if f.Kind == faults.Reorder && br.b.Rand() < 0.5 {
					if m2, ok2 := p.at(next + 1); ok2 {
						m, m2 = m2, m
						_ = m2
					}
				}
			}
		}
		if d := br.b.Config.Duration("delivery_delay_ms", 0); d > 0 {
			time.Sleep(d)
		}
		req := wire.Request{Saga: m.Saga, Step: m.Step, Payload: m.Payload, Trace: m.Trace}
		res := c.Call(ctx, wire.Meta{Saga: m.Saga, Step: m.Step}, req)
		switch {
		case res.Err == nil:
			br.out(g.topic.name, g.edge.Edge).Inc()
			g.mu.Lock()
			g.committed[pi] = next + 1
			g.mu.Unlock()
			backoff = 50 * time.Millisecond
		case res.Outcome == "dropped" || br.b.Config.Bool("at_most_once", false):
			// the network ate it (drop fault) or delivery is at-most-once:
			// move on, the message is lost
			br.lost.Inc()
			g.mu.Lock()
			g.committed[pi] = next + 1
			g.mu.Unlock()
		default:
			br.redeliv.Inc()
			time.Sleep(backoff)
			if backoff < 2*time.Second {
				backoff *= 2
			}
		}
		br.lagGauge(g.topic.name, g.edge.Edge).Set(g.lag())
	}
}

func (br *broker) retentionLoop() {
	for range time.Tick(5 * time.Second) {
		minTs := time.Now().Add(-br.b.Config.Duration("log_retention_ms", 10*time.Minute))
		br.mu.Lock()
		topics := make([]*topic, 0, len(br.topics))
		for _, t := range br.topics {
			topics = append(topics, t)
		}
		br.mu.Unlock()
		for _, t := range topics {
			for _, p := range t.partitions {
				p.retain(minTs)
			}
		}
	}
}
