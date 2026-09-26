package spec

// FaultKind is the class of disturbance injected into a node or link.
// Container-level kinds go through the runtime (Docker); the rest are
// handed to the block's /control endpoint (see blocks/common/faults).
type FaultKind string

const (
	// runtime (outside the process)
	FaultKill      FaultKind = "kill"      // stop the container abruptly, restart after duration
	FaultPause     FaultKind = "pause"     // freeze the process (SIGSTOP-like)
	FaultPartition FaultKind = "partition" // cut the network: whole node (Docker) or selected peers (block)
	// in-process chaos (block /control chaos.*)
	FaultCPU    FaultKind = "cpu_stress"      // burn CPU inside the container
	FaultMemory FaultKind = "memory_pressure" // allocate memory inside the container
	FaultStall  FaultKind = "stall"           // freeze the data path, admin port keeps answering
	FaultCrash  FaultKind = "crash"           // the process exits
	// link faults (block /control fault.set, scoped to an edge when params.edge is set)
	FaultLatency   FaultKind = "latency"   // add delay
	FaultDrop      FaultKind = "drop"      // drop a fraction of requests/messages
	FaultError     FaultKind = "error"     // answer with an error status
	FaultTimeout   FaultKind = "timeout"   // hang until the caller gives up
	FaultSlow      FaultKind = "slow"      // throttle the response body
	FaultCorrupt   FaultKind = "corrupt"   // malformed responses
	FaultDuplicate FaultKind = "duplicate" // deliver twice
	FaultReject    FaultKind = "reject"    // refuse connections
)

var AllFaultKinds = []FaultKind{
	FaultKill, FaultPause, FaultPartition,
	FaultCPU, FaultMemory, FaultStall, FaultCrash,
	FaultLatency, FaultDrop, FaultError, FaultTimeout, FaultSlow, FaultCorrupt, FaultDuplicate, FaultReject,
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
	return k == FaultCPU || k == FaultMemory || k == FaultStall || k == FaultCrash
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
