// Package spec defines the topology domain model shared by the planner, the
// engine and the frontend. These types are the wire format: they are
// serialised to JSON on disk and mirrored into TypeScript by the Wails
// binding generator (src/client/wailsjs/go/models.ts).
package spec

// NodeKind classifies what a node runs. It drives ordering in the planner
// (databases and brokers start before services) and rendering on the canvas.
type NodeKind string

const (
	KindService NodeKind = "service"
	KindBroker  NodeKind = "broker"
	KindDB      NodeKind = "db"
	KindProxy   NodeKind = "proxy" // fault-injection proxy (toxiproxy-style)
	KindInfra   NodeKind = "infra" // gateway, collector, anything supporting
)

// AllNodeKinds lists every valid kind, in planner start order.
var AllNodeKinds = []NodeKind{KindDB, KindBroker, KindProxy, KindInfra, KindService}

func (k NodeKind) Valid() bool {
	for _, v := range AllNodeKinds {
		if k == v {
			return true
		}
	}
	return false
}

// StartPriority orders node creation: lower starts first.
func (k NodeKind) StartPriority() int {
	for i, v := range AllNodeKinds {
		if k == v {
			return i
		}
	}
	return len(AllNodeKinds)
}

// EdgeKind describes the relationship between two nodes. Direction follows
// data flow: `publishes` goes producer -> broker, `subscribes` goes
// broker -> consumer, `calls` goes caller -> callee, `uses` goes client -> db.
type EdgeKind string

const (
	EdgePublishes  EdgeKind = "publishes"
	EdgeSubscribes EdgeKind = "subscribes"
	EdgeCalls      EdgeKind = "calls"
	EdgeUses       EdgeKind = "uses"
)

var AllEdgeKinds = []EdgeKind{EdgePublishes, EdgeSubscribes, EdgeCalls, EdgeUses}

func (k EdgeKind) Valid() bool {
	for _, v := range AllEdgeKinds {
		if k == v {
			return true
		}
	}
	return false
}

// Messaging reports whether the edge kind carries a topic.
func (k EdgeKind) Messaging() bool {
	return k == EdgePublishes || k == EdgeSubscribes
}

// Position is the node's location on the canvas, in React Flow coordinates.
type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Config splits a node's settings by how a change is applied.
//
//   - Hot keys can be pushed to a running container without restarting it
//     (the planner emits a `control` operation).
//   - Cold keys are baked into the container at creation time; changing one
//     forces a destroy + create (the planner classifies it as `cold`).
type Config struct {
	Hot  map[string]any `json:"hot"`
	Cold map[string]any `json:"cold"`
}

// Node is one deployable unit of the topology. Replicas > 1 means the
// runtime creates that many identically configured containers.
type Node struct {
	ID       string   `json:"id"`
	Kind     NodeKind `json:"kind"`
	Image    string   `json:"image"`
	Replicas int      `json:"replicas"`
	Position Position `json:"position"`
	Config   Config   `json:"config"`
}

// Edge connects two nodes. Topic is required for messaging edges. Via names a
// proxy node the traffic is routed through, which is where faults get
// injected for that link.
type Edge struct {
	ID    string   `json:"id"`
	From  string   `json:"from"`
	To    string   `json:"to"`
	Kind  EdgeKind `json:"kind"`
	Topic string   `json:"topic,omitempty"`
	Via   string   `json:"via,omitempty"`
}

// Topology is the full desired-state graph for one benchmark scenario.
type Topology struct {
	Version int    `json:"version"`
	Name    string `json:"name"`
	Nodes   []Node `json:"nodes"`
	Edges   []Edge `json:"edges"`
}

// NodeByID looks a node up by id.
func (t Topology) NodeByID(id string) (Node, bool) {
	for _, n := range t.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}

// Topics returns the set of topics declared on messaging edges, keyed by the
// broker node id they belong to. A messaging edge may touch a proxy instead of
// the broker itself; the proxy is followed to the broker behind it.
func (t Topology) Topics() map[string][]string {
	out := map[string][]string{}
	seen := map[string]bool{}
	for _, e := range t.Edges {
		if !e.Kind.Messaging() || e.Topic == "" {
			continue
		}
		broker, ok := t.BrokerBehind(e)
		if !ok {
			continue
		}
		key := broker + "/" + e.Topic
		if seen[key] {
			continue
		}
		seen[key] = true
		out[broker] = append(out[broker], e.Topic)
	}
	return out
}

// BrokerBehind returns the broker a messaging edge ultimately talks to. The
// broker end of the edge (`to` for publishes, `from` for subscribes) is either
// the broker itself or a proxy; a proxy is followed along its own messaging
// edge of the same kind and topic, so traffic can be routed through any number
// of proxies. ok is false for non-messaging edges and for chains that never
// reach a broker.
func (t Topology) BrokerBehind(e Edge) (string, bool) {
	if !e.Kind.Messaging() {
		return "", false
	}
	visited := map[string]bool{}
	for {
		id := e.To
		if e.Kind == EdgeSubscribes {
			id = e.From
		}
		node, ok := t.NodeByID(id)
		if !ok || visited[id] {
			return "", false
		}
		visited[id] = true
		switch node.Kind {
		case KindBroker:
			return id, true
		case KindProxy:
			next, found := t.proxyUpstream(id, e.Kind, e.Topic)
			if !found {
				return "", false
			}
			e = next
		default:
			return "", false
		}
	}
}

// proxyUpstream finds the edge that feeds a proxy for the given kind and
// topic: for publishes the edge leaving the proxy, for subscribes the edge
// entering it.
func (t Topology) proxyUpstream(proxy string, kind EdgeKind, topic string) (Edge, bool) {
	for _, e := range t.Edges {
		if e.Kind != kind || e.Topic != topic {
			continue
		}
		if kind == EdgePublishes && e.From == proxy {
			return e, true
		}
		if kind == EdgeSubscribes && e.To == proxy {
			return e, true
		}
	}
	return Edge{}, false
}

// Slug turns a topology name into an identifier safe for container and
// network names: lower-case ASCII letters, digits and single dashes.
func Slug(name string) string {
	var b []byte
	dash := true // suppress a leading dash
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b = append(b, byte(r))
			dash = false
		case r >= 'A' && r <= 'Z':
			b = append(b, byte(r-'A'+'a'))
			dash = false
		default:
			if !dash {
				b = append(b, '-')
				dash = true
			}
		}
	}
	for len(b) > 0 && b[len(b)-1] == '-' {
		b = b[:len(b)-1]
	}
	if len(b) == 0 {
		return "topology"
	}
	return string(b)
}

// Clone returns a deep copy (config maps are copied one level deep, which is
// all the schema allows).
func (t Topology) Clone() Topology {
	out := t
	out.Nodes = make([]Node, len(t.Nodes))
	for i, n := range t.Nodes {
		n.Config.Hot = copyMap(n.Config.Hot)
		n.Config.Cold = copyMap(n.Config.Cold)
		out.Nodes[i] = n
	}
	out.Edges = append([]Edge(nil), t.Edges...)
	return out
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
