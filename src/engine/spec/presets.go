package spec

// Presets returns the seeded example topologies shown in the sidebar. Both
// model the same order saga (order -> payment -> inventory) so the two
// coordination styles can be benchmarked against each other. Every node
// runs one of Symphony's block images; the proxy sits physically in the
// payment path so link faults have a place to live.
func Presets() []Topology {
	return []Topology{orchestrationSaga(), choreographySaga()}
}

// DefaultConfig is the config a new node of a kind starts with, split into
// hot keys (pushed to the running block through /control config.set) and
// cold keys (baked in at creation; changing one recreates the container).
func DefaultConfig(k NodeKind) Config {
	switch k {
	case KindService:
		return Config{
			Hot: map[string]any{
				"timeout_ms":        800,
				"retry_max":         3,
				"breaker_threshold": 5,
				"cpu_ms":            2,
				"io_ms":             0,
				"log_level":         "info",
			},
			Cold: map[string]any{"workers": 32, "queue": 64},
		}
	case KindBroker:
		return Config{
			Hot:  map[string]any{"log_retention_ms": 600000, "max_poll_records": 500, "delivery_delay_ms": 0},
			Cold: map[string]any{"num_partitions": 3},
		}
	case KindDB:
		return Config{
			Hot:  map[string]any{"statement_timeout_ms": 5000, "log_min_duration_ms": 250, "read_ms": 1, "write_ms": 2, "fsync_ms": 3},
			Cold: map[string]any{"max_connections": 100, "max_rows": 1000000},
		}
	case KindProxy:
		return Config{
			Hot:  map[string]any{"timeout_ms": 2000, "log_level": "info"},
			Cold: map[string]any{},
		}
	case KindInfra:
		return Config{
			Hot:  map[string]any{"rate_limit_rps": 0, "saga_timeout_ms": 10000, "log_level": "warn"},
			Cold: map[string]any{},
		}
	}
	return Config{Hot: map[string]any{}, Cold: map[string]any{}}
}

// NewNode builds a node of a kind with the defaults for it.
func NewNode(id string, k NodeKind, pos Position) Node {
	return Node{ID: id, Kind: k, Image: DefaultImage(k), Replicas: 1, Position: pos, Config: DefaultConfig(k), Resources: DefaultResources(k)}
}

func orchestrationSaga() Topology {
	t := Topology{
		Version: CurrentVersion,
		Name:    "Orchestration (saga)",
		Nodes: []Node{
			NewNode("gateway", KindInfra, Position{X: 40, Y: 220}),
			NewNode("orchestrator", KindService, Position{X: 300, Y: 220}),
			NewNode("toxiproxy", KindProxy, Position{X: 530, Y: 60}),
			withReplicas(NewNode("payment", KindService, Position{X: 760, Y: 100}), 2),
			NewNode("inventory", KindService, Position{X: 760, Y: 240}),
			NewNode("shipping", KindService, Position{X: 760, Y: 380}),
			NewNode("postgres", KindDB, Position{X: 300, Y: 420}),
		},
		Edges: []Edge{
			{ID: "e-gw-orch", From: "gateway", To: "orchestrator", Kind: EdgeCalls},
			// payment calls are routed through the proxy, which is where faults
			// on that link get injected
			{ID: "e-orch-proxy", From: "orchestrator", To: "toxiproxy", Kind: EdgeCalls},
			{ID: "e-proxy-pay", From: "toxiproxy", To: "payment", Kind: EdgeCalls},
			{ID: "e-orch-inv", From: "orchestrator", To: "inventory", Kind: EdgeCalls},
			{ID: "e-orch-ship", From: "orchestrator", To: "shipping", Kind: EdgeCalls},
			{ID: "e-orch-db", From: "orchestrator", To: "postgres", Kind: EdgeUses},
		},
	}
	return t.Normalized()
}

func choreographySaga() Topology {
	t := Topology{
		Version: CurrentVersion,
		Name:    "Choreography (saga)",
		Nodes: []Node{
			NewNode("gateway", KindInfra, Position{X: 40, Y: 220}),
			NewNode("order", KindService, Position{X: 300, Y: 220}),
			NewNode("kafka", KindBroker, Position{X: 560, Y: 220}),
			NewNode("toxiproxy", KindProxy, Position{X: 790, Y: 20}),
			withReplicas(NewNode("payment", KindService, Position{X: 1020, Y: 120}), 2),
			NewNode("inventory", KindService, Position{X: 820, Y: 340}),
			NewNode("postgres", KindDB, Position{X: 300, Y: 420}),
		},
		Edges: []Edge{
			{ID: "e-gw-order", From: "gateway", To: "order", Kind: EdgeCalls},
			{ID: "e-order-pub", From: "order", To: "kafka", Kind: EdgePublishes, Topic: "order.created"},
			// payment's subscription is routed through the proxy, which is where
			// faults on that link get injected
			{ID: "e-proxy-sub", From: "kafka", To: "toxiproxy", Kind: EdgeSubscribes, Topic: "order.created"},
			{ID: "e-pay-sub", From: "toxiproxy", To: "payment", Kind: EdgeSubscribes, Topic: "order.created"},
			{ID: "e-pay-pub", From: "payment", To: "kafka", Kind: EdgePublishes, Topic: "payment.completed"},
			{ID: "e-inv-sub", From: "kafka", To: "inventory", Kind: EdgeSubscribes, Topic: "payment.completed"},
			{ID: "e-inv-pub", From: "inventory", To: "kafka", Kind: EdgePublishes, Topic: "inventory.reserved"},
			{ID: "e-order-sub", From: "kafka", To: "order", Kind: EdgeSubscribes, Topic: "inventory.reserved"},
			{ID: "e-order-db", From: "order", To: "postgres", Kind: EdgeUses},
		},
	}
	return t.Normalized()
}

func withReplicas(n Node, r int) Node {
	n.Replicas = r
	return n
}
