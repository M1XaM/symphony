// Package metrics produces the realtime sample stream shown in the inspector
// chart. Today it is a mock random walk seeded from engine state; the real
// implementation will pull from the load generator and broker admin APIs
// and keep the same MetricsPayload shape.
package metrics

import (
	"context"
	"math"
	"math/rand"
	"sync"
	"time"

	"symphony/engine/core"
	"symphony/engine/events"
)

// Ticker emits events.MetricsTick on a fixed interval.
type Ticker struct {
	bus      *events.Bus
	interval time.Duration
	inputs   func() core.MetricInputs

	mu     sync.Mutex
	cancel context.CancelFunc
	rng    *rand.Rand
	base   float64
	window []float64
}

func NewTicker(bus *events.Bus, interval time.Duration, inputs func() core.MetricInputs) *Ticker {
	return &Ticker{
		bus:      bus,
		interval: interval,
		inputs:   inputs,
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
