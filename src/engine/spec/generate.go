package spec

import (
	"fmt"
	"math"
	"math/rand"
)

// What a generated saga's parts may be called.
var (
	genSteps   = []string{"payment", "inventory", "shipping", "billing", "pricing", "fraud", "loyalty", "notification", "invoice", "catalog", "cart", "tax", "refund", "email"}
	genBrokers = []string{"kafka", "rabbitmq", "nats", "pulsar", "sqs", "redis", "kinesis", "activemq", "eventhub", "pubsub"}
	genDBs     = []string{"postgres", "mysql", "mongodb"}
)

// A generated topology is laid out as the tree it is, left to right: a
// column per hop and a row per leaf, every node level with the middle of
// what hangs off it.
const (
	genCol = 270.0
	genRow = 105.0
	// no step is more hops of the saga away from the entry than this, so a
	// long saga branches rather than running off the map
	genDepth = 3
	// how many brokers the choreography end has
	genBrokersMost = 6
)

// genListeners is the fewest event-driven steps that put n brokers to use:
// each broker needs a service speaking through it and one listening, and a
// chain of events is at most genDepth steps long.
func genListeners(brokers int) int {
	if brokers == 0 {
		return 0
	}
	// the entry speaks through the first; every further broker takes a step
	// that speaks, and the last step of a chain only listens
	speaking := brokers - 1
	chains := (speaking + genDepth - 2) / (genDepth - 1)
	return maxInt(1, speaking+chains)
}

// Generate draws a random saga topology. bias runs from 0 (orchestration:
// every step is called, by the entry service or by a step that orchestrates
// its own part, and there is no broker) to 1 (choreography: every step
// reacts to an event). The number of brokers grows with it in a straight
// line, up to genBrokersMost, and so do the steps that react to events
// rather than being called; every service that is listened to speaks through
// a broker of its own. How big the result is otherwise, from a couple of
// steps to twenty nodes with databases and proxies, is drawn anew on every
// call.
//
// The result is a tree: the gateway, the entry service, then whatever each
// service calls, the broker it publishes to with that broker's listeners,
// and its database. It is laid out as that tree, so its edges run left to
// right without crossing.
//
// The edges follow what the blocks derive their behaviour from (see
// blocks/common/behaviour), so the result runs as it is: a service that is
// called calls its own steps in order, one that hears an event publishes its
// own, and the ones nothing listens to report the saga's outcome.
func Generate(bias float64, name string, rnd *rand.Rand) Topology {
	p := math.Min(1, math.Max(0, bias))
	chance := func(q float64) bool { return rnd.Float64() < q }
	// one draw decides how simple or complex this one is
	size := rnd.Float64()

	// The brokers follow the bias in a straight line: none at orchestration,
	// genBrokersMost at choreography, and in between the two whole numbers
	// around bias*genBrokersMost, the nearer one the more often.
	brokers := int(p*genBrokersMost + rnd.Float64())
	if brokers > genBrokersMost {
		brokers = genBrokersMost
	}

	minSteps := 2 + int(math.Round(p))
	maxSteps := 5 + int(math.Round(4*p))
	count := minSteps + int(math.Round(size*float64(maxSteps-minSteps)))
	// how many of the steps react to events: their share of the bias, and
	// at least as many as that number of brokers takes
	events := 0
	if brokers > 0 {
		events = maxInt(int(math.Round(p*float64(count))), genListeners(brokers))
		count = maxInt(count, events)
	}

	pool := append([]string(nil), genSteps...)
	rnd.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	called, chained := pool[events:count], pool[:events]
	entry := "orchestrator"
	if len(chained) > 0 {
		entry = "order"
	}

	// who each step answers to, and how many hops into the saga that puts it
	hops := map[string]int{entry: 0}
	source := map[string]string{}
	for i, s := range called {
		// mostly the entry; sometimes one of the last few, which then
		// orchestrates a part of its own
		src := entry
		if i > 0 && chance(0.15+0.35*size) {
			if c := called[maxInt(0, i-3)+rnd.Intn(minInt(i, 3))]; hops[c] < genDepth {
				src = c
			}
		}
		source[s] = src
		hops[s] = hops[src] + 1
	}
	// A broker stands behind every service that is listened to, the entry
	// first. So until there are as many speakers as brokers, a step listens
	// to one nobody listens to yet, which makes that one speak; from then on
	// it joins the listeners of one that speaks already. Steps to spare may
	// do the latter early, which is where the fan-outs come from.
	speakers := []string{entry}
	speaks := map[string]bool{entry: true}
	spare := len(chained) - genListeners(brokers)
	for i, s := range chained {
		var quiet []string
		for _, c := range chained[:i] {
			if !speaks[c] && hops[c] < genDepth {
				quiet = append(quiet, c)
			}
		}
		src := entry
		switch {
		case len(speakers) == brokers || spare > 0 && chance(0.4):
			if len(speakers) < brokers {
				spare--
			}
			src = speakers[rnd.Intn(len(speakers))]
		case len(quiet) > 0:
			src = quiet[rnd.Intn(len(quiet))]
			speaks[src] = true
			speakers = append(speakers, src)
		}
		source[s] = src
		hops[s] = hops[src] + 1
	}

	t := Topology{Version: CurrentVersion, Name: name}
	// the tree the layout follows: what hangs off each node, top to bottom
	kids := map[string][]string{}
	add := func(under, id string, k NodeKind) *Node {
		t.Nodes = append(t.Nodes, NewNode(id, k, Position{}))
		if under != "" {
			kids[under] = append(kids[under], id)
		}
		return &t.Nodes[len(t.Nodes)-1]
	}
	edgeIDs := map[string]bool{}
	link := func(from, to string, kind EdgeKind, topic string) {
		id := "e-" + from + "-" + to
		if kind.Messaging() {
			id += "-" + string(kind)[:3]
		}
		for n := 2; edgeIDs[id]; n++ {
			id = fmt.Sprintf("e-%s-%s-%d", from, to, n)
		}
		edgeIDs[id] = true
		t.Edges = append(t.Edges, Edge{ID: id, From: from, To: to, Kind: kind, Topic: topic})
	}

	// a proxy stands in front of a step, on the edge that reaches it
	steps := append(append([]string(nil), called...), chained...)
	proxied := map[string]bool{}
	for _, i := range rnd.Perm(len(steps))[:minInt(len(steps), rnd.Intn(2+int(math.Round(size))))] {
		proxied[steps[i]] = true
	}
	proxies := 0
	// step puts a step on the map under what reaches it, through a proxy
	// when it has one
	step := func(from, id string, kind EdgeKind, topic string) {
		if proxied[id] {
			px := uniqueName("toxiproxy", proxies)
			proxies++
			add(from, px, KindProxy)
			link(from, px, kind, topic)
			from = px
		}
		n := add(from, id, KindService)
		switch {
		case chance(0.04 + 0.08*size):
			n.Replicas = 3
		case chance(0.2):
			n.Replicas = 2
		}
		link(from, id, kind, topic)
	}

	add("", "gateway", KindInfra)
	add("gateway", entry, KindService)
	link("gateway", entry, EdgeCalls, "")

	// what each service calls comes first under it...
	for _, s := range called {
		step(source[s], s, EdgeCalls, "")
	}

	// ...then the broker it publishes to, with the services listening there...
	listeners := map[string][]string{}
	for _, s := range chained {
		listeners[source[s]] = append(listeners[source[s]], s)
	}
	for i, src := range speakers {
		if len(listeners[src]) == 0 {
			continue
		}
		topic := src + ".done"
		if src == entry {
			topic = src + ".created"
		}
		broker := genBrokers[i]
		add(src, broker, KindBroker)
		link(src, broker, EdgePublishes, topic)
		for _, s := range listeners[src] {
			step(broker, s, EdgeSubscribes, topic)
		}
	}

	// ...and last its database, if it keeps one
	services := append([]string{entry}, steps...)
	dbs := minInt(len(genDBs), rnd.Intn(2+int(math.Round(2*size))))
	for i, at := range rnd.Perm(len(services))[:minInt(dbs, len(services))] {
		owner := services[at]
		if i == 0 && chance(0.6) {
			owner = entry
		}
		add(owner, genDBs[i], KindDB)
		link(owner, genDBs[i], EdgeUses, "")
	}

	// --- layout: a leaf takes the next row, a parent the middle of its own ----
	at := map[string]*Position{}
	for i := range t.Nodes {
		at[t.Nodes[i].ID] = &t.Nodes[i].Position
	}
	row := 0.0
	var place func(id string, col int) float64
	place = func(id string, col int) float64 {
		y := row
		if below := kids[id]; len(below) == 0 {
			row += genRow
		} else {
			first := place(below[0], col+1)
			last := first
			for _, k := range below[1:] {
				last = place(k, col+1)
			}
			y = (first + last) / 2
		}
		*at[id] = Position{X: 40 + genCol*float64(col), Y: math.Round(40 + y)}
		return y
	}
	place("gateway", 0)
	return t.Normalized()
}

// uniqueName is base, then base-2, base-3, ...
func uniqueName(base string, i int) string {
	if i == 0 {
		return base
	}
	return fmt.Sprintf("%s-%d", base, i+1)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
