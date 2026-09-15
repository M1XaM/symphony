package planner

import (
	"fmt"
	"sort"
	"strings"

	"symphony/blocks/common/links"
	"symphony/engine/spec"
)

// DataPort and AdminPort are the ports every block listens on inside its
// container (see blocks/common/server).
const (
	DataPort  = 8080
	AdminPort = 9090
)

// ContainerName is the runtime name of one replica of a node: the
// deployment slug keeps two deployments that share node ids apart.
func ContainerName(slug, nodeID string, replica, total int) string {
	if total <= 1 {
		return slug + "-" + nodeID
	}
	return fmt.Sprintf("%s-%s-%d", slug, nodeID, replica)
}

// ContainerNames lists every replica's container name.
func ContainerNames(slug string, n spec.Node) []string {
	out := make([]string, 0, n.Replicas)
	for r := 1; r <= n.Replicas; r++ {
		out = append(out, ContainerName(slug, n.ID, r, n.Replicas))
	}
	return out
}

// LinkSet is what goes into a node's SYMPHONY_LINKS: every edge touching
// it, with the data-port URLs of the peer's replicas on outbound edges,
// proxy pairs, and the gateway's admin URL for outcome reports.
func LinkSet(t spec.Topology, nodeID, slug string) links.Set {
	set := links.Set{In: []links.Link{}, Out: []links.Link{}}
	for _, e := range t.Edges {
		l := links.Link{Edge: e.ID, Kind: string(e.Kind), From: e.From, To: e.To, Topic: e.Topic, TimeoutMs: e.TimeoutMs, Retries: e.Retries}
		switch {
		case e.From == nodeID:
			if peer, ok := t.NodeByID(e.To); ok {
				for _, name := range ContainerNames(slug, peer) {
					l.Targets = append(l.Targets, fmt.Sprintf("http://%s:%d", name, DataPort))
				}
			}
			set.Out = append(set.Out, l)
		case e.To == nodeID:
			set.In = append(set.In, l)
		}
	}
	if n, ok := t.NodeByID(nodeID); ok && n.Kind == spec.KindProxy {
		set.Pairs = proxyPairs(set)
	}
	for _, n := range t.Nodes {
		if n.Kind == spec.KindInfra {
			set.Gateway = fmt.Sprintf("http://%s:%d", ContainerName(slug, n.ID, 1, n.Replicas), AdminPort)
			break
		}
	}
	return set
}

// proxyPairs joins each inbound edge of a proxy to the outbound edge that
// continues it: same topic for messaging edges, otherwise the same kind in
// declaration order.
func proxyPairs(set links.Set) []links.Pair {
	used := map[string]bool{}
	var pairs []links.Pair
	for _, in := range set.In {
		for _, out := range set.Out {
			if used[out.Edge] || out.Kind != in.Kind {
				continue
			}
			if in.Topic != "" && out.Topic != in.Topic {
				continue
			}
			used[out.Edge] = true
			pairs = append(pairs, links.Pair{In: in.Edge, Out: out.Edge})
			break
		}
	}
	return pairs
}

// LinkSignature summarises a node's wiring so the planner can tell when
// it changed. Links are baked into the container's environment, so a
// change is a cold change for both ends of the edge.
func LinkSignature(t spec.Topology, nodeID, slug string) string {
	set := LinkSet(t, nodeID, slug)
	parts := []string{}
	for _, l := range set.In {
		parts = append(parts, fmt.Sprintf("in:%s:%s:%s:%s:%d:%d", l.Edge, l.Kind, l.From, l.Topic, l.TimeoutMs, l.Retries))
	}
	for _, l := range set.Out {
		parts = append(parts, fmt.Sprintf("out:%s:%s:%s:%s:%d:%d:%s", l.Edge, l.Kind, l.To, l.Topic, l.TimeoutMs, l.Retries, strings.Join(l.Targets, ",")))
	}
	for _, p := range set.Pairs {
		parts = append(parts, "pair:"+p.In+">"+p.Out)
	}
	parts = append(parts, "gw:"+set.Gateway)
	sort.Strings(parts)
	return strings.Join(parts, ";")
}
