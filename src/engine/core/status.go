// Node status: the runtime's container states and the blocks' deep health
// folded into one state per node, refreshed by events and by the poller.

package core

import (
	"context"
	"fmt"
	"sync"
	"time"

	"symphony/blocks/common/health"
	"symphony/engine/events"
	"symphony/engine/planner"
	"symphony/engine/runtime"
)

// NodeStatus is the aggregated runtime state of one topology node across
// its replicas. State is the "worst" replica state; Checks come from the
// blocks' deep health.
type NodeStatus struct {
	NodeID    string               `json:"node_id"`
	State     string               `json:"state"` // pending | starting | healthy | degraded | failed | stopped | paused
	Replicas  int                  `json:"replicas"`
	Ready     int                  `json:"ready"`
	Message   string               `json:"message,omitempty"`
	UpdatedAt int64                `json:"updated_at"`
	Checks    []events.HealthCheck `json:"checks,omitempty"`
}

type statSample struct {
	runtime.Stats
	at time.Time
}

func (e *Engine) onContainerState(c runtime.Container) {
	e.mu.Lock()
	ref, ok := e.containers[c.Name]
	d := e.deps[ref.dep]
	if ok && d != nil {
		d.states[c.Name] = c
	}
	e.mu.Unlock()
	if !ok || d == nil {
		return
	}
	if c.OOMKilled {
		e.log("error", "runtime", fmt.Sprintf("[%s] %s was OOM-killed", d.name, c.Name))
	}
	e.recomputeStatus(d, ref.node)
}

// recomputeStatus folds the runtime states and the blocks' health documents
// of a node's replicas into one NodeStatus.
func (e *Engine) recomputeStatus(d *deployment, nodeID string) {
	names := e.containersFor(d.name, nodeID)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ready := 0
	worst := runtime.StateHealthy
	var checks []events.HealthCheck
	msg := ""
	for _, n := range names {
		e.mu.Lock()
		c, cached := d.states[n]
		cl := e.clients[n]
		e.mu.Unlock()
		if !cached {
			var err error
			c, err = e.rt.Inspect(ctx, n)
			if err != nil {
				continue
			}
			e.mu.Lock()
			d.states[n] = c
			e.mu.Unlock()
		}
		state := c.State
		if c.OOMKilled {
			msg = "OOM killed"
		}
		if cl != nil && (state == runtime.StateHealthy || state == runtime.StateStarting) {
			doc, err := cl.Health(ctx)
			switch {
			case err != nil:
				state = runtime.StateStarting
			case doc.Status == health.Failing:
				state = runtime.StateFailed
			case doc.Status == health.Degraded:
				state = "degraded"
			default:
				state = runtime.StateHealthy
			}
			for _, ch := range doc.Checks {
				name := ch.Name
				if len(names) > 1 {
					name = fmt.Sprintf("%d/%s", doc.Replica, ch.Name)
				}
				checks = append(checks, events.HealthCheck{Name: name, Status: string(ch.Status), Detail: ch.Detail})
			}
		}
		if state == runtime.StateHealthy || state == "degraded" {
			ready++
		}
		if stateRank(state) > stateRank(worst) {
			worst = state
		}
	}
	e.mu.Lock()
	st := d.statuses[nodeID]
	if st == nil {
		e.mu.Unlock()
		return
	}
	replicas := st.Replicas
	if msg == "" {
		msg = st.Message
	}
	e.mu.Unlock()
	e.setStatus(d, nodeID, string(worst), replicas, ready, msg, checks)
}

// stateRank orders states by "badness" so a node reports its worst replica.
func stateRank(s runtime.State) int {
	switch s {
	case runtime.StateHealthy:
		return 0
	case "degraded":
		return 1
	case runtime.StateStarting:
		return 2
	case runtime.StatePending:
		return 3
	case runtime.StatePaused:
		return 4
	case runtime.StateStopped:
		return 5
	case runtime.StateFailed:
		return 6
	}
	return 7
}

func (e *Engine) setStatus(d *deployment, nodeID, state string, replicas, ready int, msg string, checks []events.HealthCheck) {
	e.mu.Lock()
	st := &NodeStatus{NodeID: nodeID, State: state, Replicas: replicas, Ready: ready, Message: msg, UpdatedAt: time.Now().UnixMilli(), Checks: checks}
	d.statuses[nodeID] = st
	payload := statusPayload(d.name, *st)
	e.mu.Unlock()
	e.bus.Publish(events.NodeStatus, payload)
}

func (e *Engine) setStatusMessage(d *deployment, nodeID, msg string) {
	e.mu.Lock()
	st := d.statuses[nodeID]
	if st == nil {
		e.mu.Unlock()
		return
	}
	st.Message = msg
	st.UpdatedAt = time.Now().UnixMilli()
	payload := statusPayload(d.name, *st)
	e.mu.Unlock()
	e.bus.Publish(events.NodeStatus, payload)
}

func statusPayload(dep string, st NodeStatus) events.NodeStatusPayload {
	return events.NodeStatusPayload{
		Deployment: dep, NodeID: st.NodeID, State: st.State, Replicas: st.Replicas,
		Ready: st.Ready, Message: st.Message, UpdatedAt: st.UpdatedAt, Checks: st.Checks,
	}
}

// pollLoop refreshes deep health and container stats every second.
// With FollowStore it also looks at the store every third second.
func (e *Engine) pollLoop(ctx context.Context) {
	tk := time.NewTicker(pollInterval)
	defer tk.Stop()
	for n := 1; ; n++ {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			if e.follow && n%3 == 0 {
				e.syncStore(ctx)
			}
			e.pollOnce(ctx)
		}
	}
}

func (e *Engine) pollOnce(ctx context.Context) {
	e.mu.Lock()
	type job struct {
		d    *deployment
		node string
	}
	var jobs []job
	var containers []struct {
		d    *deployment
		node string
		name string
	}
	for _, d := range e.deps {
		if d.phase != PhaseRunning && d.phase != PhaseFailed {
			continue
		}
		for _, n := range d.current.Nodes {
			jobs = append(jobs, job{d, n.ID})
			for _, name := range planner.ContainerNames(d.slug, n) {
				if e.containers[name].dep == d.name {
					containers = append(containers, struct {
						d    *deployment
						node string
						name string
					}{d, n.ID, name})
				}
			}
		}
	}
	e.mu.Unlock()

	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(j job) {
			defer wg.Done()
			defer func() { <-sem }()
			e.recomputeStatus(j.d, j.node)
		}(j)
	}
	for _, c := range containers {
		wg.Add(1)
		sem <- struct{}{}
		go func(d *deployment, node, name string) {
			defer wg.Done()
			defer func() { <-sem }()
			st, err := e.rt.Stats(ctx, name)
			if err != nil {
				return
			}
			now := time.Now()
			e.mu.Lock()
			prev, had := d.stats[name]
			d.stats[name] = statSample{Stats: st, at: now}
			e.mu.Unlock()
			p := events.ContainerStatsPayload{Deployment: d.name, NodeID: node, Container: name, Ts: now.UnixMilli(),
				CPUPct: round1(st.CPUPercent), MemUsed: st.MemoryBytes, MemLimit: st.MemoryLimit, Throttled: st.ThrottledPeriods}
			if had {
				secs := now.Sub(prev.at).Seconds()
				if secs > 0 && st.NetRxBytes >= prev.NetRxBytes && st.NetTxBytes >= prev.NetTxBytes {
					p.NetRxBps = float64(st.NetRxBytes-prev.NetRxBytes) / secs
					p.NetTxBps = float64(st.NetTxBytes-prev.NetTxBytes) / secs
				}
			}
			e.bus.Publish(events.ContainerStats, p)
		}(c.d, c.node, c.name)
	}
	wg.Wait()
}

func round1(x float64) float64 { return float64(int(x*10)) / 10 }
