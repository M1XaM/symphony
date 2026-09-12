package spec

// FaultKind is the class of disturbance injected into a node or link.
type FaultKind string

const (
	FaultLatency   FaultKind = "latency"    // add delay on the node's proxied links
	FaultDrop      FaultKind = "drop"       // drop a fraction of requests/messages
	FaultKill      FaultKind = "kill"       // stop the container abruptly
	FaultPause     FaultKind = "pause"      // freeze the process (SIGSTOP-like)
	FaultPartition FaultKind = "partition"  // cut network to selected peers
	FaultCPU       FaultKind = "cpu_stress" // burn CPU inside the container
)

var AllFaultKinds = []FaultKind{FaultLatency, FaultDrop, FaultKill, FaultPause, FaultPartition, FaultCPU}

func (k FaultKind) Valid() bool {
	for _, v := range AllFaultKinds {
		if k == v {
			return true
		}
	}
	return false
}

// FaultSpec describes one injected fault. DurationMs of 0 means "until
// cleared". Params carry kind-specific knobs, e.g. {"ms": 250, "jitter_ms": 50}
// for latency or {"rate": 0.1} for drop.
type FaultSpec struct {
	Kind       FaultKind      `json:"kind"`
	DurationMs int            `json:"duration_ms"`
	Params     map[string]any `json:"params"`
}
