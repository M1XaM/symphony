package spec

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
)

func TestGenerateIsValidAlongTheWholeScale(t *testing.T) {
	for step := 0; step <= 50; step++ {
		for seed := int64(1); seed <= 60; seed++ {
			topo := Generate(float64(step)/50, "x", rand.New(rand.NewSource(seed*100+int64(step))))
			if findings := topo.Validate(); len(findings) > 0 {
				t.Fatalf("step %d seed %d: %v", step, seed, findings)
			}
			gateways := 0
			at := map[string]Position{}
			for _, n := range topo.Nodes {
				at[n.ID] = n.Position
				if n.Kind == KindInfra {
					gateways++
				}
			}
			// no two nodes touch (a node is about 172 by 80 on the map)
			for i, a := range topo.Nodes {
				for _, b := range topo.Nodes[i+1:] {
					if math.Abs(a.Position.X-b.Position.X) < 180 && math.Abs(a.Position.Y-b.Position.Y) < 90 {
						t.Fatalf("step %d seed %d: %s and %s overlap", step, seed, a.ID, b.ID)
					}
				}
			}
			// every edge runs one column to the right
			for _, e := range topo.Edges {
				dx := at[e.To].X - at[e.From].X
				if dx <= 0 {
					t.Fatalf("step %d seed %d: %s runs backwards", step, seed, e.ID)
				}
				if dx > genCol {
					t.Fatalf("step %d seed %d: %s skips a column", step, seed, e.ID)
				}
			}
			if gateways != 1 {
				t.Fatalf("step %d seed %d: %d gateways", step, seed, gateways)
			}
			// everything but the gateway is reached by something
			reached := map[string]bool{}
			for _, e := range topo.Edges {
				reached[e.To] = true
			}
			for _, n := range topo.Nodes {
				if n.Kind != KindInfra && !reached[n.ID] {
					t.Fatalf("step %d seed %d: nothing reaches %s", step, seed, n.ID)
				}
			}
		}
	}
}

// The two ends are the two styles, and choreography is the bigger one: more
// nodes, and a broker for every service that others listen to.
func TestGenerateLeansWithTheBias(t *testing.T) {
	const runs = 300
	count := func(bias float64) (nodes, brokers float64, sizes map[int]bool) {
		sizes = map[int]bool{}
		for seed := int64(1); seed <= runs; seed++ {
			topo := Generate(bias, "x", rand.New(rand.NewSource(seed)))
			nodes += float64(len(topo.Nodes))
			sizes[len(topo.Nodes)] = true
			for _, n := range topo.Nodes {
				if n.Kind == KindBroker {
					brokers++
				}
			}
			speakers := map[string]int{}
			for _, e := range topo.Edges {
				if bias == 0 && e.Kind.Messaging() {
					t.Fatalf("seed %d: an orchestration with a %s edge", seed, e.Kind)
				}
				if bias == 1 && e.Kind == EdgeCalls && e.From != "gateway" {
					t.Fatalf("seed %d: a choreography where %s calls %s", seed, e.From, e.To)
				}
				if e.Kind == EdgePublishes {
					speakers[e.To]++
				}
			}
			for b, n := range speakers {
				if n != 1 {
					t.Fatalf("seed %d: %d services share the broker %s", seed, n, b)
				}
			}
		}
		return nodes / runs, brokers / runs, sizes
	}
	orchNodes, orchBrokers, orchSizes := count(0)
	midNodes, midBrokers, _ := count(0.5)
	chorNodes, chorBrokers, chorSizes := count(1)
	t.Logf("nodes %.1f / %.1f / %.1f, brokers %.2f / %.2f / %.2f", orchNodes, midNodes, chorNodes, orchBrokers, midBrokers, chorBrokers)
	if orchBrokers != 0 {
		t.Errorf("orchestration has %.2f brokers on average, want none", orchBrokers)
	}
	if midBrokers != genBrokersMost/2 || chorBrokers != genBrokersMost {
		t.Errorf("brokers on average: %.2f in the middle, %.2f at choreography", midBrokers, chorBrokers)
	}
	if !(chorNodes > midNodes && midNodes > orchNodes) {
		t.Errorf("nodes on average: %.1f, %.1f, %.1f from orchestration to choreography", orchNodes, midNodes, chorNodes)
	}
	// simple ones and complex ones both come up
	if len(orchSizes) < 5 || len(chorSizes) < 5 {
		t.Errorf("sizes seen: %d at orchestration, %d at choreography", len(orchSizes), len(chorSizes))
	}
}

// The number of brokers follows the bias in a straight line, and at any one
// place on the scale it is one of the two whole numbers around that line.
func TestGenerateBrokersFollowTheBias(t *testing.T) {
	const runs = 400
	for step := 0; step <= 50; step++ {
		bias := float64(step) / 50
		want := bias * genBrokersMost
		sum := 0.0
		for seed := int64(1); seed <= runs; seed++ {
			brokers := 0.0
			for _, n := range Generate(bias, "x", rand.New(rand.NewSource(seed*53+int64(step)))).Nodes {
				if n.Kind == KindBroker {
					brokers++
				}
			}
			if brokers < math.Floor(want) || brokers > math.Ceil(want) {
				t.Fatalf("step %d seed %d: %v brokers, want about %.2f", step, seed, brokers, want)
			}
			sum += brokers
		}
		if mean := sum / runs; math.Abs(mean-want) > 0.15 {
			t.Errorf("step %d: %.2f brokers on average, want %.2f", step, mean, want)
		}
	}
}

func TestGenerateIsRepeatableForASeed(t *testing.T) {
	a := Generate(0.7, "x", rand.New(rand.NewSource(42)))
	b := Generate(0.7, "x", rand.New(rand.NewSource(42)))
	if fmt.Sprint(a) != fmt.Sprint(b) {
		t.Fatal("the same seed drew two different topologies")
	}
}
