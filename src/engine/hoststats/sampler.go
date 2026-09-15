// Package hoststats samples the host machine's CPU, memory and network usage
// and publishes it as events.HostStats for the Stats tab. It keeps a short
// history so the tab has a graph the moment it opens.
package hoststats

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"

	"symphony/engine/events"
)

// HistoryCap bounds the samples kept for late subscribers; at one sample a
// second this is the last few minutes.
const HistoryCap = 180

// Sampler publishes one HostStatsPayload per interval.
type Sampler struct {
	bus      *events.Bus
	interval time.Duration

	mu      sync.Mutex
	cancel  context.CancelFunc
	history []events.HostStatsPayload
	lastNet *netSnapshot
}

type netSnapshot struct {
	rx, tx uint64
	at     time.Time
}

func New(bus *events.Bus, interval time.Duration) *Sampler {
	return &Sampler{bus: bus, interval: interval}
}

func (s *Sampler) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	ctx, s.cancel = context.WithCancel(ctx)
	go s.loop(ctx)
}

func (s *Sampler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}

// History returns the buffered samples, oldest first.
func (s *Sampler) History() []events.HostStatsPayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append(make([]events.HostStatsPayload, 0, len(s.history)), s.history...)
}

func (s *Sampler) loop(ctx context.Context) {
	// Prime the CPU and network deltas so the first published sample is a
	// real rate rather than "since boot".
	_, _ = cpu.PercentWithContext(ctx, 0, false)
	s.snapshotNet(ctx)

	tk := time.NewTicker(s.interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			p := s.sample(ctx)
			s.mu.Lock()
			if len(s.history) >= HistoryCap {
				s.history = append(s.history[:0], s.history[len(s.history)-HistoryCap+1:]...)
			}
			s.history = append(s.history, p)
			s.mu.Unlock()
			s.bus.Publish(events.HostStats, p)
		}
	}
}

func (s *Sampler) sample(ctx context.Context) events.HostStatsPayload {
	p := events.HostStatsPayload{Ts: time.Now().UnixMilli()}
	if pct, err := cpu.PercentWithContext(ctx, 0, false); err == nil && len(pct) > 0 {
		p.CPUPct = round1(pct[0])
	}
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		p.MemPct = round1(vm.UsedPercent)
		p.MemUsed = vm.Used
		p.MemTotal = vm.Total
	}
	prev := s.lastNet
	cur := s.snapshotNet(ctx)
	if prev != nil && cur != nil {
		secs := cur.at.Sub(prev.at).Seconds()
		if secs > 0 {
			p.NetRxBps = math.Round(float64(cur.rx-prev.rx) / secs)
			p.NetTxBps = math.Round(float64(cur.tx-prev.tx) / secs)
		}
	}
	return p
}

// snapshotNet records the summed counters of every interface.
func (s *Sampler) snapshotNet(ctx context.Context) *netSnapshot {
	counters, err := net.IOCountersWithContext(ctx, false)
	if err != nil || len(counters) == 0 {
		return nil
	}
	snap := &netSnapshot{rx: counters[0].BytesRecv, tx: counters[0].BytesSent, at: time.Now()}
	s.mu.Lock()
	s.lastNet = snap
	s.mu.Unlock()
	return snap
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }
