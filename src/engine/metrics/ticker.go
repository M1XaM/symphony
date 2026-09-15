// Package metrics produces the realtime sample streams shown in the UI: the
// global run metrics (metrics:tick) and the per-edge link health that colours
// the map (link:health). Today both are mocks seeded from engine state; the
// real implementation will pull from the load generator, the proxies and the
// broker admin APIs and keep the same payload shapes.
package metrics

import (
	"context"
	"math"
	"math/rand"
	"sync"
	"time"

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

// Ticker emits events.MetricsTick and events.LinkHealth on a fixed interval.
type Ticker struct {
	bus      *events.Bus
	interval time.Duration
	inputs   func() core.MetricInputs
	links    func() []core.LinkInput

	mu     sync.Mutex
	cancel context.CancelFunc
	rng    *rand.Rand
	base   float64
	window []float64
}

func NewTicker(bus *events.Bus, interval time.Duration, inputs func() core.MetricInputs, links func() []core.LinkInput) *Ticker {
	return &Ticker{
		bus:      bus,
		interval: interval,
		inputs:   inputs,
		links:    links,
		rng:      rand.New(rand.NewSource(time.Now().UnixNano())),
		base:     42,
	}
}

func (t *Ticker) Start(ctx context.Context) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cancel != nil {
		return
	}
	ctx, t.cancel = context.WithCancel(ctx)
	go t.loop(ctx)
}

func (t *Ticker) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
}

func (t *Ticker) loop(ctx context.Context) {
	tk := time.NewTicker(t.interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			t.bus.Publish(events.MetricsTick, t.sample())
			for _, p := range t.sampleLinks() {
				t.bus.Publish(events.LinkHealth, p)
			}
		}
	}
}

func (t *Ticker) sample() events.MetricsPayload {
	in := t.inputs()

	// Random walk around a baseline that rises with load and with faults.
	target := 42.0
	if in.Running {
		target = 55 + 6*float64(in.Total-in.Healthy)
	}
	target += 90 * float64(in.ActiveFaults)
	t.base += (target - t.base) * 0.15
	latency := t.base + t.rng.NormFloat64()*4
	if t.rng.Float64() < 0.04 { // occasional tail spike
		latency += 40 + t.rng.Float64()*80
	}
	latency = math.Max(1, latency)

	t.window = append(t.window, latency)
	if len(t.window) > 120 {
		t.window = t.window[1:]
	}

	throughput := 0.0
	if in.Running && in.Healthy > 0 {
		throughput = 120*float64(in.Healthy)/math.Max(1, float64(in.Total)) + t.rng.NormFloat64()*6
	}
	lag := 0
	if in.Running {
		lag = int(math.Max(0, 12+float64(in.ActiveFaults)*300+t.rng.NormFloat64()*8))
	}
	cpu := 4 + t.rng.NormFloat64()*1.5
	if in.Running {
		cpu = 18 + 7*float64(in.Healthy) + t.rng.NormFloat64()*3
	}

	return events.MetricsPayload{
		Ts:           time.Now().UnixMilli(),
		LatencyMs:    round1(latency),
		P99Ms:        round1(percentile(t.window, 0.99)),
		ConsumerLag:  lag,
		SagasPerSec:  round1(math.Max(0, throughput)),
		CPUPct:       round1(math.Min(100, math.Max(0, cpu))),
		ActiveFaults: in.ActiveFaults,
	}
}

// sampleLinks derives an error rate for every edge of every live deployment
// from the state of the nodes at its ends and the faults on them. Replace
// with real counters (proxy toxics, consumer offsets) keeping the payload.
func (t *Ticker) sampleLinks() []events.LinkHealthPayload {
	byDep := map[string]*events.LinkHealthPayload{}
	var order []string
	now := time.Now().UnixMilli()
	for _, in := range t.links() {
		p := byDep[in.Deployment]
		if p == nil {
			p = &events.LinkHealthPayload{Deployment: in.Deployment, Ts: now, Links: map[string]events.LinkHealthEntry{}}
			byDep[in.Deployment] = p
			order = append(order, in.Deployment)
		}
		rate := t.errorRate(in)
		p.Links[in.EdgeID] = events.LinkHealthEntry{ErrorRate: math.Round(rate*1000) / 1000, Level: Level(rate)}
	}
	out := make([]events.LinkHealthPayload, 0, len(order))
	for _, name := range order {
		out = append(out, *byDep[name])
	}
	return out
}

func (t *Ticker) errorRate(in core.LinkInput) float64 {
	// A healthy link still drops the odd request: 0.2%-1.5% background noise.
	rate := 0.002 + t.rng.Float64()*0.013
	for _, s := range in.States {
		switch s {
		case "healthy":
		case "starting", "pending":
			rate += 0.10 + t.rng.Float64()*0.10 // some replicas not answering yet
		default: // paused, stopped, failed
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
		case spec.FaultLatency:
			rate += 0.06 + t.rng.Float64()*0.04 // slow answers time out
		case spec.FaultCPU:
			rate += 0.02 + t.rng.Float64()*0.03
		case spec.FaultPartition, spec.FaultKill, spec.FaultPause:
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
	for i := 1; i < len(sorted); i++ { // insertion sort; n <= 120
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
