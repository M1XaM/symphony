package spec

import (
	"symphony/blocks/common/control"
	"symphony/blocks/common/faults"
)

// FaultKind is the class of disturbance injected into a node or link. There
// are three families, by who carries the fault out:
//
//   - runtime kinds are done to the container from outside, through Docker;
//   - chaos kinds are done by the block to itself (/control chaos.*);
//   - link kinds are the blocks' own vocabulary (blocks/common/faults),
//     applied per request and scoped to an edge when params.edge is set.
//     They are not listed a second time here: every kind the blocks know is
//     a FaultKind.
type FaultKind string

const (
	// runtime (outside the process)
	FaultKill      FaultKind = "kill"      // stop the container abruptly, start it again after the duration
	FaultPause     FaultKind = "pause"     // freeze the process (SIGSTOP-like)
	FaultPartition FaultKind = "partition" // cut the network: whole node (Docker) or selected peers (block)
	// in-process chaos
	FaultCPU    FaultKind = "cpu_stress"      // burn CPU inside the container
	FaultMemory FaultKind = "memory_pressure" // allocate memory inside the container
	FaultStall  FaultKind = "stall"           // freeze the data path, admin port keeps answering
	FaultCrash  FaultKind = "crash"           // the process exits; started again after the duration
	// link faults, by the names the engine refers to them with
	FaultLatency   = FaultKind(faults.Latency)   // add delay
	FaultDrop      = FaultKind(faults.Drop)      // drop a fraction of requests/messages
	FaultError     = FaultKind(faults.Error)     // answer with an error status
	FaultTimeout   = FaultKind(faults.Timeout)   // hang until the caller gives up
	FaultSlow      = FaultKind(faults.Slow)      // throttle the response body
	FaultCorrupt   = FaultKind(faults.Corrupt)   // malformed responses
	FaultDuplicate = FaultKind(faults.Duplicate) // deliver twice
	FaultReorder   = FaultKind(faults.Reorder)   // deliver out of order (broker)
	FaultReject    = FaultKind(faults.Reject)    // refuse connections
)

// chaosOps maps the chaos kinds to the control operation that starts them.
var chaosOps = map[FaultKind]string{
	FaultCPU:    control.OpChaosCPU,
	FaultMemory: control.OpChaosMemory,
	FaultStall:  control.OpChaosStall,
	FaultCrash:  control.OpChaosCrash,
}

// AllFaultKinds lists every valid kind: the runtime and chaos kinds, then
// the blocks' link kinds.
var AllFaultKinds = allFaultKinds()

func allFaultKinds() []FaultKind {
	out := []FaultKind{FaultKill, FaultPause, FaultPartition, FaultCPU, FaultMemory, FaultStall, FaultCrash}
	for _, k := range faults.Kinds {
		if FaultKind(k) != FaultPartition { // listed above: it is both
			out = append(out, FaultKind(k))
		}
	}
	return out
}

func (k FaultKind) Valid() bool {
	for _, v := range AllFaultKinds {
		if k == v {
			return true
		}
	}
	return false
}

// Chaos reports whether the fault is one of the in-process chaos ops.
func (k FaultKind) Chaos() bool {
	return chaosOps[k] != ""
}

// ChaosOp is the control operation that starts a chaos kind, "" for the
// other kinds.
func (k FaultKind) ChaosOp() string {
	return chaosOps[k]
}

// FaultSpec describes one injected fault. DurationMs of 0 means "until
// cleared". Params carry kind-specific knobs, e.g. {"ms": 250, "jitter_ms": 50}
// for latency or {"rate": 0.1} for drop, plus the optional scope keys
// "edge", "direction" (in|out) and "peer" for link faults.
type FaultSpec struct {
	Kind       FaultKind      `json:"kind"`
	DurationMs int            `json:"duration_ms"`
	Params     map[string]any `json:"params"`
}

// Selective reports whether a partition cuts only the peers named in
// params.peers (the block does that) rather than the whole node (Docker).
func (f FaultSpec) Selective() bool {
	_, ok := f.Params["peers"]
	return f.Kind == FaultPartition && ok
}

// NeedsRevert reports whether somebody has to end the fault: Docker does
// not unpause, restart or reconnect a container by itself, and a process
// that exited does not come back. The other kinds expire inside the block.
func (f FaultSpec) NeedsRevert() bool {
	switch f.Kind {
	case FaultKill, FaultPause, FaultCrash:
		return true
	case FaultPartition:
		return !f.Selective()
	}
	return false
}
