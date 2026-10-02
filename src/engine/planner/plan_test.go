package planner

import (
	"strings"
	"testing"

	"symphony/engine/spec"
)

func kinds(p Plan) string {
	var out []string
	for _, op := range p.Ops {
		out = append(out, string(op.Kind)+":"+op.Target)
	}
	return strings.Join(out, " ")
}

func empty(name string) spec.Topology {
	return spec.Topology{Version: spec.CurrentVersion, Name: name}.Normalized()
}

// A first apply creates the stores before the things that use them, and
// the topics before their subscribers start.
func TestDiffFirstApplyOrder(t *testing.T) {
	topo := spec.Presets()[1] // choreography: db, broker, proxy, gateway, services
	plan := Diff(empty(topo.Name), topo)
	if plan.Network != "symphony-choreography-saga" {
		t.Fatalf("network %q", plan.Network)
	}
	pos := map[string]int{}
	for i, op := range plan.Ops {
		pos[string(op.Kind)+":"+op.Target] = i
		if op.Class != ClassStructural {
			t.Errorf("%s %s is %s on a first apply", op.Kind, op.Target, op.Class)
		}
	}
	for _, pair := range [][2]string{
		{"ensure_network:" + plan.Network, "create:postgres"},
		{"create:postgres", "create:kafka"},
		{"wait_healthy:kafka", "ensure_topic:kafka/order.created"},
		{"ensure_topic:kafka/order.created", "create:payment"},
		{"create:gateway", "create:order"},
	} {
		a, okA := pos[pair[0]]
		b, okB := pos[pair[1]]
		if !okA || !okB || a >= b {
			t.Errorf("%s must come before %s in: %s", pair[0], pair[1], kinds(plan))
		}
	}
	if plan.Summary.Creates != len(topo.Nodes) || plan.Summary.Destroys != 0 {
		t.Errorf("summary %+v", plan.Summary)
	}
	if again := Diff(topo, topo); len(again.Ops) != 0 {
		t.Errorf("applying the same topology again plans %s", kinds(again))
	}
}

func change(t spec.Topology, id string, fn func(*spec.Node)) spec.Topology {
	out := t.Clone()
	for i := range out.Nodes {
		if out.Nodes[i].ID == id {
			fn(&out.Nodes[i])
		}
	}
	return out
}

func TestDiffClassifiesChanges(t *testing.T) {
	base := spec.Presets()[0] // orchestration
	for _, tc := range []struct {
		name string
		to   spec.Topology
		want string
	}{
		{"hot config is pushed in place",
			change(base, "payment", func(n *spec.Node) { n.Config.Hot["timeout_ms"] = 1500 }),
			"control:payment"},
		{"limits are updated in place",
			change(base, "payment", func(n *spec.Node) { n.Resources.CPUs = 0.1 }),
			"update_resources:payment"},
		{"a behaviour program is pushed in place",
			change(base, "inventory", func(n *spec.Node) { n.Behaviour = []byte(`{"handlers":[]}`) }),
			"configure:inventory"},
		{"cold config recreates the node",
			change(base, "inventory", func(n *spec.Node) { n.Config.Cold["workers"] = 8 }),
			"ensure_image:symphony/service:dev ensure_network:symphony-orchestration-saga destroy:inventory create:inventory wait_healthy:inventory"},
		// the caller's links name every replica, so it is recreated with them
		{"replicas recreate the node and whoever calls it",
			change(base, "inventory", func(n *spec.Node) { n.Replicas = 3 }),
			"ensure_image:symphony/service:dev ensure_network:symphony-orchestration-saga destroy:inventory create:inventory wait_healthy:inventory destroy:orchestrator create:orchestrator wait_healthy:orchestrator"},
	} {
		if got := kinds(Diff(base, tc.to)); got != tc.want {
			t.Errorf("%s:\n got  %s\n want %s", tc.name, got, tc.want)
		}
	}
	hot := Diff(base, change(base, "payment", func(n *spec.Node) { n.Config.Hot["timeout_ms"] = 1500 }))
	if hot.Summary.Hot != 1 || hot.Summary.Cold != 0 || hot.Summary.Structural != 0 {
		t.Errorf("a hot change summarised as %+v", hot.Summary)
	}
}

func TestDiffTeardownDestroysEverything(t *testing.T) {
	topo := spec.Presets()[0]
	plan := Diff(topo, empty(topo.Name))
	if plan.Summary.Destroys != len(topo.Nodes) || len(plan.Ops) != len(topo.Nodes) {
		t.Fatalf("teardown plans %s", kinds(plan))
	}
	// services go before the stores they depend on
	if first, last := plan.Ops[0].Target, plan.Ops[len(plan.Ops)-1].Target; first == "postgres" || last != "postgres" {
		t.Errorf("destroy order: %s", kinds(plan))
	}
}

func TestContainerNames(t *testing.T) {
	n := spec.NewNode("payment", spec.KindService, spec.Position{})
	if got := ContainerNames("demo", n); len(got) != 1 || got[0] != "demo-payment" {
		t.Errorf("one replica: %v", got)
	}
	n.Replicas = 2
	if got := ContainerNames("demo", n); len(got) != 2 || got[0] != "demo-payment-1" || got[1] != ContainerName("demo", "payment", 2, 2) {
		t.Errorf("two replicas: %v", got)
	}
}

// A proxy forwards each inbound edge to the outbound edge that continues it.
func TestLinkSetPairsProxyEdges(t *testing.T) {
	topo := spec.Presets()[0]
	set := LinkSet(topo, "toxiproxy", "demo")
	if len(set.Pairs) != 1 || set.Pairs[0].In != "e-orch-proxy" || set.Pairs[0].Out != "e-proxy-pay" {
		t.Fatalf("pairs %+v", set.Pairs)
	}
	out := set.Out[0]
	if len(out.Targets) != 2 || out.Targets[0] != "http://demo-payment-1:8080" {
		t.Errorf("targets %v", out.Targets)
	}
	if set.Gateway != "http://demo-gateway:9090" {
		t.Errorf("gateway %q", set.Gateway)
	}
}
