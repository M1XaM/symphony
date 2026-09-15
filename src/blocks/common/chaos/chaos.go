// Package chaos provokes trouble inside the process: burn CPU, hold memory,
// stall the data path or crash. These are the "what the kernel and the
// scheduler do to a misbehaving container" experiments; the network faults
// live in package faults.
package chaos

import (
	"context"
	"os"
	"runtime"
	"sync"
	"time"
)

// Burn spins goroutines that use about `cores` CPU cores until the duration
// passes or the context ends. Fractional cores are approximated with a duty
// cycle (busy 10 ms, idle the rest of the period).
func Burn(ctx context.Context, cores float64, d time.Duration) {
	if cores <= 0 {
		return
	}
	full := int(cores)
	frac := cores - float64(full)
	deadline := time.Now().Add(d)
	spin := func(duty float64) {
		for time.Now().Before(deadline) && ctx.Err() == nil {
			busyUntil := time.Now().Add(time.Duration(duty * float64(10*time.Millisecond)))
			x := 0
			for time.Now().Before(busyUntil) {
				x++
				if x%1000 == 0 {
					runtime.Gosched()
				}
			}
			if duty < 1 {
				time.Sleep(time.Duration((1 - duty) * float64(10*time.Millisecond)))
			}
		}
	}
	for i := 0; i < full; i++ {
		go spin(1)
	}
	if frac > 0.01 {
		go spin(frac)
	}
}

// Memory allocates and touches mb megabytes and holds them for d.
type Memory struct {
	mu    sync.Mutex
	held  [][]byte
	total int
}

// Hold adds mb megabytes; they are released after d (0 = keep).
func (m *Memory) Hold(mb int, d time.Duration) {
	if mb <= 0 {
		return
	}
	buf := make([]byte, mb<<20)
	for i := 0; i < len(buf); i += 4096 {
		buf[i] = 1 // touch every page so the RSS really grows
	}
	m.mu.Lock()
	m.held = append(m.held, buf)
	m.total += mb
	m.mu.Unlock()
	if d > 0 {
		time.AfterFunc(d, func() { m.Release(mb) })
	}
}

// Release frees up to mb megabytes (the oldest allocations first).
func (m *Memory) Release(mb int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for len(m.held) > 0 && mb > 0 {
		n := len(m.held[0]) >> 20
		m.held = m.held[1:]
		m.total -= n
		mb -= n
	}
	if len(m.held) == 0 {
		m.total = 0
	}
	runtime.GC()
}

// HeldMB reports how much is held.
func (m *Memory) HeldMB() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.total
}

// Gate blocks the data path while stalled. Handlers call Wait at entry.
type Gate struct {
	mu    sync.Mutex
	until time.Time
	ch    chan struct{}
}

func NewGate() *Gate {
	g := &Gate{ch: make(chan struct{})}
	close(g.ch)
	return g
}

// Stall closes the gate for d.
func (g *Gate) Stall(d time.Duration) {
	g.mu.Lock()
	g.until = time.Now().Add(d)
	if isClosed(g.ch) {
		g.ch = make(chan struct{})
	}
	ch := g.ch
	g.mu.Unlock()
	time.AfterFunc(d, func() {
		g.mu.Lock()
		if g.ch == ch && !isClosed(ch) {
			close(ch)
		}
		g.mu.Unlock()
	})
}

// Open releases a stall early.
func (g *Gate) Open() {
	g.mu.Lock()
	if !isClosed(g.ch) {
		close(g.ch)
	}
	g.until = time.Time{}
	g.mu.Unlock()
}

// Wait blocks until the gate is open or the context ends.
func (g *Gate) Wait(ctx context.Context) error {
	g.mu.Lock()
	ch := g.ch
	g.mu.Unlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stalled reports the remaining stall.
func (g *Gate) Stalled() time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	if isClosed(g.ch) {
		return 0
	}
	return time.Until(g.until)
}

func isClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// Crash exits the process after a short delay so the control reply gets out.
func Crash(code int) {
	time.AfterFunc(100*time.Millisecond, func() { os.Exit(code) })
}
