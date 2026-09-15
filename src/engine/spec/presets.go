package spec

// Presets returns the seeded example topologies shown in the sidebar. Both
// model the same order saga (order -> payment -> inventory) so the two
// coordination styles can be benchmarked against each other.
func Presets() []Topology {
	return []Topology{orchestrationSaga(), choreographySaga()}
}

// PresetByName returns the named preset, or false.
func PresetByName(name string) (Topology, bool) {
	for _, p := range Presets() {
		if p.Name == name {
			return p, true
		}
	}
	return Topology{}, false
}

func svcConfig(port int) Config {
	return Config{
		Hot: map[string]any{
			"timeout_ms":     800,
			"retry_max":      3,
			"rate_limit_rps": 200,
			"log_level":      "info",
		},
		Cold: map[string]any{
			"port":         port,
			"db_pool_size": 10,
			"heap_mb":      256,
		},
	}
}

func orchestrationSaga() Topology {
	t := Topology{
		Version: CurrentVersion,
		Name:    "Orchestration (saga)",
		Nodes: []Node{
			{ID: "gateway", Kind: KindInfra, Image: "nginx:1.27-alpine", Replicas: 1, Position: Position{X: 40, Y: 220},
				Config: Config{
					Hot:  map[string]any{"rate_limit_rps": 500, "log_level": "warn"},
					Cold: map[string]any{"port": 8080, "worker_connections": 1024},
				}},
			{ID: "orchestrator", Kind: KindService, Image: "symphony/orchestrator:dev", Replicas: 1, Position: Position{X: 300, Y: 220}, Config: svcConfig(9000)},
			{ID: "toxiproxy", Kind: KindProxy, Image: "ghcr.io/shopify/toxiproxy:2.9.0", Replicas: 1, Position: Position{X: 530, Y: 60},
				Config: Config{
					Hot:  map[string]any{"latency_ms": 0, "jitter_ms": 0, "drop_rate": 0.0},
					Cold: map[string]any{"api_port": 8474},
				}},
			{ID: "payment", Kind: KindService, Image: "symphony/payment:dev", Replicas: 2, Position: Position{X: 760, Y: 100}, Config: svcConfig(9001)},
			{ID: "inventory", Kind: KindService, Image: "symphony/inventory:dev", Replicas: 1, Position: Position{X: 760, Y: 240}, Config: svcConfig(9002)},
			{ID: "shipping", Kind: KindService, Image: "symphony/shipping:dev", Replicas: 1, Position: Position{X: 760, Y: 380}, Config: svcConfig(9003)},
			{ID: "postgres", Kind: KindDB, Image: "postgres:16-alpine", Replicas: 1, Position: Position{X: 300, Y: 420},
				Config: Config{
					Hot:  map[string]any{"statement_timeout_ms": 5000, "log_min_duration_ms": 250},
					Cold: map[string]any{"max_connections": 100, "shared_buffers_mb": 128},
				}},
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
			{ID: "gateway", Kind: KindInfra, Image: "nginx:1.27-alpine", Replicas: 1, Position: Position{X: 40, Y: 220},
				Config: Config{
					Hot:  map[string]any{"rate_limit_rps": 500, "log_level": "warn"},
					Cold: map[string]any{"port": 8080, "worker_connections": 1024},
				}},
			{ID: "order", Kind: KindService, Image: "symphony/order:dev", Replicas: 1, Position: Position{X: 300, Y: 220}, Config: svcConfig(9000)},
			{ID: "kafka", Kind: KindBroker, Image: "apache/kafka:3.8.0", Replicas: 1, Position: Position{X: 560, Y: 220},
				Config: Config{
					Hot:  map[string]any{"log_retention_ms": 600000, "max_poll_records": 500},
					Cold: map[string]any{"num_partitions": 3, "replication_factor": 1, "heap_mb": 512},
				}},
			{ID: "toxiproxy", Kind: KindProxy, Image: "ghcr.io/shopify/toxiproxy:2.9.0", Replicas: 1, Position: Position{X: 790, Y: 20},
				Config: Config{
					Hot:  map[string]any{"latency_ms": 0, "jitter_ms": 0, "drop_rate": 0.0},
					Cold: map[string]any{"api_port": 8474},
				}},
			{ID: "payment", Kind: KindService, Image: "symphony/payment:dev", Replicas: 2, Position: Position{X: 1020, Y: 120}, Config: svcConfig(9001)},
			{ID: "inventory", Kind: KindService, Image: "symphony/inventory:dev", Replicas: 1, Position: Position{X: 820, Y: 340}, Config: svcConfig(9002)},
			{ID: "postgres", Kind: KindDB, Image: "postgres:16-alpine", Replicas: 1, Position: Position{X: 300, Y: 420},
				Config: Config{
					Hot:  map[string]any{"statement_timeout_ms": 5000, "log_min_duration_ms": 250},
					Cold: map[string]any{"max_connections": 100, "shared_buffers_mb": 128},
				}},
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
