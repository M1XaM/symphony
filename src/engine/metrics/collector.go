// Package metrics produces the realtime sample streams shown in the UI: the
// per-deployment run metrics (metrics:tick) and the per-edge link health
// that colours the map (link:health). The numbers come from the blocks' own
// counters, scraped every interval.
package metrics

import (
	"context"
	"math"
	"sync"
	"time"

	bmetrics "symphony/blocks/common/metrics"
	"symphony/engine/blockclient"
	"symphony/engine/core"
	"symphony/engine/events"
)

// Thresholds on a link's error rate (share of requests or messages that were
// not delivered or failed) for the map colours: below WarnThreshold is green,
// from WarnThreshold up is yellow, from CritThreshold up is red.
const (
	WarnThreshold = 0.05
	CritThreshold = 0.25
)

// Level classifies an error rate against the thresholds.
func Level(errorRate float64) events.LinkLevel {
	switch {
	case errorRate >= CritThreshold:
		return events.LinkCrit
	case errorRate >= WarnThreshold:
		return events.LinkWarn
	}
	return events.LinkOK
}

// Collector emits events.MetricsTick and events.LinkHealth on a fixed interval.
type Collector struct {
	bus      *events.Bus
	interval time.Duration
	eng      *core.Engine

	mu     sync.Mutex
	cancel context.CancelFunc
	// last scrape per container, for deltas
	prev map[*blockclient.Client]scrape
	// error-rate memory per edge so a link without traffic keeps its colour
	lastLink map[string]map[string]events.LinkHealthEntry
}

type scrape struct {
	at  time.Time
	doc bmetrics.Document
}

func NewCollector(bus *events.Bus, interval time.Duration, eng *core.Engine) *Collector {
	return &Collector{
		bus:      bus,
		interval: interval,
		eng:      eng,
		prev:     map[*blockclient.Client]scrape{},
		lastLink: map[string]map[string]events.LinkHealthEntry{},
	}
}

func (c *Collector) Start(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		return
	}
	ctx, c.cancel = context.WithCancel(ctx)
	go c.loop(ctx)
}

func (c *Collector) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
}

func (c *Collector) loop(ctx context.Context) {
	tk := time.NewTicker(c.interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			c.tick(ctx)
		}
	}
}

func (c *Collector) tick(ctx context.Context) {
	snaps := c.eng.Snapshot()
	now := time.Now().UnixMilli()
	all := events.MetricsPayload{Deployment: "", Ts: now}
	var aggLat []float64
	for _, snap := range snaps {
		p, links := c.scrapeDeployment(ctx, snap)
		p.Ts, links.Ts = now, now
		c.bus.Publish(events.MetricsTick, p)
		c.bus.Publish(events.LinkHealth, links)
		all.SagasPerSec += p.SagasPerSec
		all.ConsumerLag += p.ConsumerLag
		all.CPUPct += p.CPUPct
		all.ActiveFaults += p.ActiveFaults
		all.SagasOK += p.SagasOK
		all.SagasFailed += p.SagasFailed
		if p.LatencyMs > 0 {
			aggLat = append(aggLat, p.LatencyMs)
		}
		if p.P99Ms > all.P99Ms {
			all.P99Ms = p.P99Ms
		}
	}
	if len(aggLat) > 0 {
		s := 0.0
		for _, v := range aggLat {
			s += v
		}
		all.LatencyMs = round1(s / float64(len(aggLat)))
	}
	if all.SagasOK+all.SagasFailed > 0 {
		all.ErrorRate = round3(float64(all.SagasFailed) / float64(all.SagasOK+all.SagasFailed))
	}
	c.bus.Publish(events.MetricsTick, all)
	c.prune(snaps)
}

// prune forgets the deltas of clients and deployments that are no longer
// live. Without it a session that applies and tears down all evening keeps a
// scraped document for every container it ever started.
func (c *Collector) prune(snaps []core.DeploymentSnapshot) {
	liveClients := map[*blockclient.Client]bool{}
	liveDeps := map[string]bool{}
	for _, snap := range snaps {
		liveDeps[snap.Name] = true
		for _, n := range snap.Nodes {
			for _, cl := range n.Clients {
				liveClients[cl] = true
			}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for cl := range c.prev {
		if !liveClients[cl] {
			delete(c.prev, cl)
		}
	}
	for name := range c.lastLink {
		if !liveDeps[name] {
			delete(c.lastLink, name)
		}
	}
}

// --- real path -------------------------------------------------------------

type delta struct {
	total, ok, failed float64
	p99               float64
	secs              float64
}

// scrapeDeployment reads every block's counters and derives the deployment's
// sample and per-edge link health from the deltas since the last scrape.
func (c *Collector) scrapeDeployment(ctx context.Context, snap core.DeploymentSnapshot) (events.MetricsPayload, events.LinkHealthPayload) {
	p := events.MetricsPayload{Deployment: snap.Name, ActiveFaults: snap.Faults}
	links := events.LinkHealthPayload{Deployment: snap.Name, Links: map[string]events.LinkHealthEntry{}}
	perEdge := map[string]*delta{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	var sagaOK, sagaFail float64
	var sagaLat, sagaP99 float64
	var lag float64
	sagaSeen := false

	for _, n := range snap.Nodes {
		p.CPUPct += n.CPUPct
		for _, cl := range n.Clients {
			wg.Add(1)
			sem <- struct{}{}
			go func(n core.NodeSnapshot, cl *blockclient.Client) {
				defer wg.Done()
				defer func() { <-sem }()
				cctx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
				defer cancel()
				doc, err := cl.Metrics(cctx)
				if err != nil {
					return
				}
				now := time.Now()
				c.mu.Lock()
				prev, had := c.prev[cl]
				c.prev[cl] = scrape{at: now, doc: doc}
				c.mu.Unlock()
				secs := c.interval.Seconds()
				if had {
					secs = now.Sub(prev.at).Seconds()
				}
				mu.Lock()
				defer mu.Unlock()
				// per-edge outbound results from the sender
				for _, ctr := range doc.Counters {
					switch ctr.Name {
					case "link_results_total":
						if ctr.Labels["dir"] != "out" {
							continue
						}
						edge := ctr.Labels["edge"]
						d := perEdge[edge]
						if d == nil {
							d = &delta{secs: secs}
							perEdge[edge] = d
						}
						v := float64(ctr.Value)
						if had {
							v -= float64(counterValue(prev.doc, ctr.Name, ctr.Labels))
						}
						d.total += v
						if ctr.Labels["result"] == "ok" {
							d.ok += v
						} else {
							d.failed += v
						}
					case "saga_total":
						sagaSeen = true
						v := float64(ctr.Value)
						if had {
							v -= float64(counterValue(prev.doc, ctr.Name, ctr.Labels))
						}
						if ctr.Labels["outcome"] == "ok" {
							sagaOK += v
						} else if ctr.Labels["outcome"] != "rejected" {
							sagaFail += v
						}
					case "broker_lag":
						lag += float64(ctr.Value)
					}
				}
				for _, h := range doc.Histograms {
					switch h.Name {
					case "link_latency_ms":
						if h.Labels["dir"] != "out" {
							continue
						}
						d := perEdge[h.Labels["edge"]]
						if d == nil {
							d = &delta{secs: secs}
							perEdge[h.Labels["edge"]] = d
						}
						if h.P99 > d.p99 {
							d.p99 = h.P99
						}
					case "saga_latency_ms":
						if h.P50 > sagaLat {
							sagaLat = h.P50
						}
						if h.P99 > sagaP99 {
							sagaP99 = h.P99
						}
					}
				}
			}(n, cl)
		}
	}
	wg.Wait()

	c.mu.Lock()
	remembered := c.lastLink[snap.Name]
	if remembered == nil {
		remembered = map[string]events.LinkHealthEntry{}
		c.lastLink[snap.Name] = remembered
	}
	c.mu.Unlock()
	for _, e := range snap.Topology.Edges {
		d := perEdge[e.ID]
		entry := events.LinkHealthEntry{Level: events.LinkOK}
		if d != nil && d.total > 0 {
			rate := d.failed / d.total
			entry = events.LinkHealthEntry{ErrorRate: round3(rate), Level: Level(rate), RPS: round1(d.total / math.Max(d.secs, 0.001)), P99Ms: d.p99}
		} else if prev, ok := remembered[e.ID]; ok {
			// no traffic in this window: keep the last verdict, show zero rate
			entry = events.LinkHealthEntry{ErrorRate: prev.ErrorRate, Level: prev.Level, RPS: 0, P99Ms: prev.P99Ms}
		}
		// a fault on either end paints the link at least yellow
		if from := snap.Nodes[e.From]; from.Faults > 0 && entry.Level == events.LinkOK {
			entry.Level = events.LinkWarn
		}
		if to := snap.Nodes[e.To]; to.Faults > 0 && entry.Level == events.LinkOK {
			entry.Level = events.LinkWarn
		}
		links.Links[e.ID] = entry
		remembered[e.ID] = entry
	}

	secs := c.interval.Seconds()
	if sagaSeen {
		p.SagasPerSec = round1((sagaOK + sagaFail) / secs)
		p.SagasOK = int64(sagaOK)
		p.SagasFailed = int64(sagaFail)
		if sagaOK+sagaFail > 0 {
			p.ErrorRate = round3(sagaFail / (sagaOK + sagaFail))
		}
		p.LatencyMs = sagaLat
		p.P99Ms = sagaP99
	}
	p.ConsumerLag = int(lag)
	p.CPUPct = round1(p.CPUPct)
	return p, links
}

func counterValue(doc bmetrics.Document, name string, labels bmetrics.Labels) int64 {
	for _, ctr := range doc.Counters {
		if ctr.Name != name || len(ctr.Labels) != len(labels) {
			continue
		}
		match := true
		for k, v := range labels {
			if ctr.Labels[k] != v {
				match = false
				break
			}
		}
		if match {
			return ctr.Value
		}
	}
	return 0
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }
func round3(x float64) float64 { return math.Round(x*1000) / 1000 }
