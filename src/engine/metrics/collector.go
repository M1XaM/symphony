// Package metrics produces the realtime sample streams shown in the UI: the
// per-deployment run metrics (metrics:tick) and the per-edge link health
// that colours the map (link:health). With block containers the numbers
// come from the blocks' own counters, scraped every interval; with the
// fake runtime a mock derived from engine state keeps the UI alive.
package metrics

import (
	"context"
	"math"
	"math/rand"
	"sync"
	"time"

	bmetrics "symphony/blocks/common/metrics"
	"symphony/engine/blockclient"
	"symphony/engine/core"
	"symphony/engine/events"
	"symphony/engine/spec"
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
	rng    *rand.Rand
	base   float64
	window []float64
	// last scrape per container, for deltas
	prev map[*blockclient.Client]scrape
	// last published sample per deployment, for the API
	last map[string]events.MetricsPayload
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
		rng:      rand.New(rand.NewSource(time.Now().UnixNano())),
		base:     42,
		prev:     map[*blockclient.Client]scrape{},
		last:     map[string]events.MetricsPayload{},
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

// Last returns the most recent sample of a deployment ("" = all).
func (c *Collector) Last(deployment string) (events.MetricsPayload, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.last[deployment]
	return p, ok
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
	all := events.MetricsPayload{Deployment: "", Ts: now, Source: "blocks"}
	anyBlocks := false
	var aggLat []float64
	for _, snap := range snaps {
		hasClients := false
		for _, n := range snap.Nodes {
			if len(n.Clients) > 0 {
				hasClients = true
				break
			}
		}
		var p events.MetricsPayload
		var links events.LinkHealthPayload
		if hasClients {
			anyBlocks = true
			p, links = c.scrapeDeployment(ctx, snap)
		} else {
			p, links = c.mockDeployment(snap)
		}
		p.Ts, links.Ts = now, now
		c.bus.Publish(events.MetricsTick, p)
		c.bus.Publish(events.LinkHealth, links)
		c.mu.Lock()
		c.last[snap.Name] = p
		c.mu.Unlock()
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
	if !anyBlocks {
		all.Source = "mock"
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
	if len(snaps) == 0 {
		all = c.idleSample(now)
	}
	c.bus.Publish(events.MetricsTick, all)
	c.mu.Lock()
	c.last[""] = all
	c.mu.Unlock()
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
	p := events.MetricsPayload{Deployment: snap.Name, Source: "blocks", ActiveFaults: snap.Faults}
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

// --- mock path (fake runtime) ----------------------------------------------

func (c *Collector) idleSample(now int64) events.MetricsPayload {
	in := c.eng.MetricInputs()
	return c.mockSample("", in, now)
}

func (c *Collector) mockSample(name string, in core.MetricInputs, now int64) events.MetricsPayload {
	c.mu.Lock()
	defer c.mu.Unlock()
	target := 42.0
	if in.Running {
		target = 55 + 6*float64(in.Total-in.Healthy)
	}
	target += 90 * float64(in.ActiveFaults)
	c.base += (target - c.base) * 0.15
	latency := c.base + c.rng.NormFloat64()*4
	if c.rng.Float64() < 0.04 {
		latency += 40 + c.rng.Float64()*80
	}
	latency = math.Max(1, latency)
	c.window = append(c.window, latency)
	if len(c.window) > 120 {
		c.window = c.window[1:]
	}
	throughput := 0.0
	if in.Running && in.Healthy > 0 {
		throughput = 120*float64(in.Healthy)/math.Max(1, float64(in.Total)) + c.rng.NormFloat64()*6
	}
	lag := 0
	if in.Running {
		lag = int(math.Max(0, 12+float64(in.ActiveFaults)*300+c.rng.NormFloat64()*8))
	}
	cpu := 4 + c.rng.NormFloat64()*1.5
	if in.Running {
		cpu = 18 + 7*float64(in.Healthy) + c.rng.NormFloat64()*3
	}
	return events.MetricsPayload{
		Deployment:   name,
		Ts:           now,
		LatencyMs:    round1(latency),
		P99Ms:        round1(percentile(c.window, 0.99)),
		ConsumerLag:  lag,
		SagasPerSec:  round1(math.Max(0, throughput)),
		CPUPct:       round1(math.Min(100, math.Max(0, cpu))),
		ActiveFaults: in.ActiveFaults,
		Source:       "mock",
	}
}

func (c *Collector) mockDeployment(snap core.DeploymentSnapshot) (events.MetricsPayload, events.LinkHealthPayload) {
	in := core.MetricInputs{Running: snap.Phase == core.PhaseRunning || snap.Phase == core.PhaseApplying, ActiveFaults: snap.Faults}
	for _, n := range snap.Nodes {
		in.Total++
		if n.State == "healthy" {
			in.Healthy++
		}
	}
	p := c.mockSample(snap.Name, in, 0)
	links := events.LinkHealthPayload{Deployment: snap.Name, Links: map[string]events.LinkHealthEntry{}}
	for _, li := range c.eng.LinkInputs() {
		if li.Deployment != snap.Name {
			continue
		}
		rate := c.mockErrorRate(li)
		links.Links[li.EdgeID] = events.LinkHealthEntry{ErrorRate: round3(rate), Level: Level(rate), RPS: round1(p.SagasPerSec)}
	}
	return p, links
}

func (c *Collector) mockErrorRate(in core.LinkInput) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	rate := 0.002 + c.rng.Float64()*0.013
	for _, s := range in.States {
		switch s {
		case "healthy", "degraded":
		case "starting", "pending":
			rate += 0.10 + c.rng.Float64()*0.10
		default:
			rate = 1
		}
	}
	for _, f := range in.Faults {
		switch f.Kind {
		case spec.FaultDrop:
			r, _ := f.Params["rate"].(float64)
			if r <= 0 {
				r = 0.1
			}
			rate += r
		case spec.FaultLatency, spec.FaultTimeout, spec.FaultError, spec.FaultSlow:
			rate += 0.06 + c.rng.Float64()*0.04
		case spec.FaultCPU, spec.FaultMemory:
			rate += 0.02 + c.rng.Float64()*0.03
		case spec.FaultPartition, spec.FaultKill, spec.FaultPause, spec.FaultStall, spec.FaultCrash:
			rate = 1
		}
	}
	return math.Min(1, math.Max(0, rate))
}

func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sorted := append([]float64(nil), xs...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j-1] > sorted[j]; j-- {
			sorted[j-1], sorted[j] = sorted[j], sorted[j-1]
		}
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	return sorted[idx]
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }
func round3(x float64) float64 { return math.Round(x*1000) / 1000 }
