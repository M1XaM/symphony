// The health checks and control operations every block has.

package server

import (
	"context"
	"fmt"
	"time"

	"symphony/blocks/common/chaos"
	"symphony/blocks/common/control"
	"symphony/blocks/common/faults"
	"symphony/blocks/common/health"
)

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

	b.Control.Handle(control.OpChaosCPU, func(cmd control.Command) (any, error) {
		cores := cmd.Float("cores", 1)
		d := cmd.Duration()
		if d <= 0 {
			d = 10 * time.Second
		}
		b.burnMu.Lock()
		if b.burnCtx == nil {
			b.burnCtx, b.burnStop = context.WithCancel(context.Background())
		}
		ctx := b.burnCtx
		b.burnMu.Unlock()
		chaos.Burn(ctx, cores, d)
		b.Events.Publish("chaos.cpu", cmd.Params)
		return map[string]any{"cores": cores, "duration_ms": d.Milliseconds()}, nil
	})
	b.Control.Handle(control.OpChaosMemory, func(cmd control.Command) (any, error) {
		mb := cmd.Int("mb", 100)
		b.Memory.Hold(mb, cmd.Duration())
		b.Events.Publish("chaos.memory", cmd.Params)
		return map[string]any{"held_mb": b.Memory.HeldMB()}, nil
	})
	b.Control.Handle(control.OpChaosStall, func(cmd control.Command) (any, error) {
		d := cmd.Duration()
		if d <= 0 {
			d = 5 * time.Second
		}
		b.Gate.Stall(d)
		b.Events.Publish("chaos.stall", map[string]any{"duration_ms": d.Milliseconds()})
		return map[string]any{"stalled_ms": d.Milliseconds()}, nil
	})
	b.Control.Handle(control.OpChaosResume, func(cmd control.Command) (any, error) {
		b.Gate.Open()
		return map[string]any{"stalled_ms": 0}, nil
	})
	// chaos.clear ends everything chaos.cpu, chaos.memory and chaos.stall
	// started, whatever duration they were given.
	b.Control.Handle(control.OpChaosClear, func(cmd control.Command) (any, error) {
		released := b.Memory.HeldMB()
		b.clearChaos()
		b.Events.Publish("chaos.clear", nil)
		return map[string]any{"stalled_ms": 0, "released_mb": released}, nil
	})
	b.Control.Handle(control.OpChaosCrash, func(cmd control.Command) (any, error) {
		code := cmd.Int("exit_code", 137)
		b.Log.Error("crashing on request", "exit_code", code)
		chaos.Crash(code)
		return map[string]any{"exit_code": code}, nil
	})
	b.Control.Handle(control.OpLogLevel, func(cmd control.Command) (any, error) {
		lv := cmd.String("level", "")
		if err := b.Logs.SetLevel(lv); err != nil {
			return nil, fmt.Errorf("%w: %v", control.ErrBadRequest, err)
		}
		return map[string]any{"level": b.Logs.Level()}, nil
	})
	b.Control.Handle(control.OpLinksGet, func(cmd control.Command) (any, error) { return b.Links, nil })
	b.Control.Handle(control.OpTargetsGet, func(cmd control.Command) (any, error) {
		out := map[string]any{}
		for _, c := range b.Outs() {
			out[c.Link.Edge] = c.TargetStates()
		}
		return out, nil
	})
	prevReset := b.Control.Reset
	b.Control.Reset = func() {
		b.Metrics.Reset()
		b.clearChaos()
		for _, c := range b.Outs() {
			c.Reset()
		}
		if prevReset != nil {
			prevReset()
		}
	}
}

// clearChaos stops the CPU burns, frees the held memory and opens the gate.
func (b *Block) clearChaos() {
	b.burnMu.Lock()
	if b.burnStop != nil {
		b.burnStop()
		b.burnCtx, b.burnStop = nil, nil
	}
	b.burnMu.Unlock()
	b.Memory.Release(1 << 30)
	b.Gate.Open()
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
