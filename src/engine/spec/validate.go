package spec

import (
	"fmt"

	"symphony/blocks/common/behaviour"
)

// Severity of a validation finding. Errors block Apply; warnings do not.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// ValidationError is one finding from Validate. Path is a JSON-pointer-ish
// location ("nodes[2].image", "edges[0].topic"); Node/Edge carry the ids the
// finding refers to so the UI can highlight them.
type ValidationError struct {
	Path     string   `json:"path"`
	Node     string   `json:"node,omitempty"`
	Edge     string   `json:"edge,omitempty"`
	Message  string   `json:"message"`
	Severity Severity `json:"severity"`
}

func (v ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", v.Path, v.Message)
}

// HasErrors reports whether any finding has error severity.
func HasErrors(errs []ValidationError) bool {
	for _, e := range errs {
		if e.Severity == SeverityError {
			return true
		}
	}
	return false
}

// idRule is what ValidID accepts, as the validation message says it.
const idRule = "use letters, digits, '-', '_' and '.', starting with a letter or digit, at most 63 characters"

// ValidID reports whether an id can be part of a container name, a DNS
// alias and a URL path, which is where node and edge ids end up.
func ValidID(id string) bool {
	if id == "" || len(id) > 63 {
		return false
	}
	for i, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case (r == '-' || r == '_' || r == '.') && i > 0:
		default:
			return false
		}
	}
	return true
}

// nameRule is what ValidName accepts, as the validation message says it.
const nameRule = "use English letters, digits, spaces and special characters only"

// ValidName reports whether a topology (template) name is made of printable
// ASCII only: English letters, digits, spaces and special characters.
func ValidName(name string) bool {
	for _, r := range name {
		if r < ' ' || r > '~' {
			return false
		}
	}
	return true
}

// ReplicaNames lists the names of a node's containers within its
// deployment: the node id, or "<id>-<n>" for each of several replicas.
func ReplicaNames(n Node) []string {
	if n.Replicas <= 1 {
		return []string{n.ID}
	}
	out := make([]string, 0, n.Replicas)
	for r := 1; r <= n.Replicas; r++ {
		out = append(out, fmt.Sprintf("%s-%d", n.ID, r))
	}
	return out
}

// Validate checks structural integrity of the topology. It never mutates the
// receiver and returns all findings rather than stopping at the first.
func (t Topology) Validate() []ValidationError {
	var errs []ValidationError
	add := func(path, node, edge, msg string) {
		errs = append(errs, ValidationError{Path: path, Node: node, Edge: edge, Message: msg, Severity: SeverityError})
	}
	warn := func(path, node, edge, msg string) {
		errs = append(errs, ValidationError{Path: path, Node: node, Edge: edge, Message: msg, Severity: SeverityWarning})
	}

	if t.Name == "" {
		add("name", "", "", "topology name is required")
	} else if !ValidName(t.Name) {
		add("name", "", "", fmt.Sprintf("topology name %q: %s", t.Name, nameRule))
	}
	if t.Version < 1 {
		add("version", "", "", "version must be >= 1")
	}
	if len(t.Nodes) == 0 {
		warn("nodes", "", "", "topology has no nodes")
	}

	byID := map[string]Node{}
	for i, n := range t.Nodes {
		p := fmt.Sprintf("nodes[%d]", i)
		if n.ID == "" {
			add(p+".id", "", "", "node id is required")
			continue
		}
		if _, dup := byID[n.ID]; dup {
			add(p+".id", n.ID, "", fmt.Sprintf("duplicate node id %q", n.ID))
			continue
		}
		byID[n.ID] = n
		if !ValidID(n.ID) {
			add(p+".id", n.ID, "", fmt.Sprintf("node id %q: %s", n.ID, idRule))
		}
		if !n.Kind.Valid() {
			add(p+".kind", n.ID, "", fmt.Sprintf("unknown node kind %q", n.Kind))
		}
		if n.Image == "" {
			add(p+".image", n.ID, "", "image is required")
		}
		if n.Replicas < 1 {
			add(p+".replicas", n.ID, "", "replicas must be >= 1")
		}
		if n.Replicas > 1 && (n.Kind == KindDB || n.Kind == KindProxy) {
			warn(p+".replicas", n.ID, "", fmt.Sprintf("%s nodes are usually single-replica", n.Kind))
		}
		for k := range n.Config.Hot {
			if _, clash := n.Config.Cold[k]; clash {
				add(p+".config", n.ID, "", fmt.Sprintf("config key %q is both hot and cold", k))
			}
		}
		if n.Resources.CPUs < 0 {
			add(p+".resources.cpus", n.ID, "", "cpus cannot be negative")
		}
		if n.Resources.MemoryMB < 0 || (n.Resources.MemoryMB > 0 && n.Resources.MemoryMB < 16) {
			add(p+".resources.memory_mb", n.ID, "", "memory_mb must be 0 (unlimited) or at least 16")
		}
		if n.Resources.PidsMax < 0 {
			add(p+".resources.pids_max", n.ID, "", "pids_max cannot be negative")
		}
		if len(n.Behaviour) > 0 {
			if n.Kind != KindService {
				warn(p+".behaviour", n.ID, "", fmt.Sprintf("behaviour programs only run on service nodes, %s ignores it", n.Kind))
			} else if _, err := behaviour.Parse(n.Behaviour); err != nil {
				add(p+".behaviour", n.ID, "", err.Error())
			}
		}
	}

	// Sinks report a saga's outcome to the deployment's gateway, and there
	// is one address for that: the first gateway's.
	var gateways []string
	for _, n := range t.Nodes {
		if n.Kind == KindInfra && n.ID != "" {
			gateways = append(gateways, n.ID)
		}
	}
	if len(gateways) > 1 {
		warn("nodes", gateways[1], "", fmt.Sprintf("more than one gateway: sagas that end asynchronously (through a broker) report their outcome to %q only, so the ones %q starts will time out",
			gateways[0], gateways[1]))
	}

	// A replica's container is named "<node>-<n>", which must not be what
	// another node (or one of its replicas) is called.
	owner := map[string]string{}
	named := map[string]bool{}
	for i, n := range t.Nodes {
		if n.ID == "" || named[n.ID] {
			continue // reported above
		}
		named[n.ID] = true
		for _, name := range ReplicaNames(n) {
			if other, taken := owner[name]; taken && other != n.ID {
				add(fmt.Sprintf("nodes[%d].id", i), n.ID, "", fmt.Sprintf("container name %q is also used by node %q; rename one of them", name, other))
				continue
			}
			owner[name] = n.ID
		}
	}

	edgeIDs := map[string]bool{}
	for i, e := range t.Edges {
		p := fmt.Sprintf("edges[%d]", i)
		if e.ID == "" {
			add(p+".id", "", "", "edge id is required")
		} else if edgeIDs[e.ID] {
			add(p+".id", "", e.ID, fmt.Sprintf("duplicate edge id %q", e.ID))
		} else if !ValidID(e.ID) {
			add(p+".id", "", e.ID, fmt.Sprintf("edge id %q: %s", e.ID, idRule))
		}
		edgeIDs[e.ID] = true

		from, okFrom := byID[e.From]
		to, okTo := byID[e.To]
		if !okFrom {
			add(p+".from", "", e.ID, fmt.Sprintf("unknown node %q", e.From))
		}
		if !okTo {
			add(p+".to", "", e.ID, fmt.Sprintf("unknown node %q", e.To))
		}
		if e.From == e.To && e.From != "" {
			add(p, "", e.ID, "edge connects a node to itself")
		}
		if !e.Kind.Valid() {
			add(p+".kind", "", e.ID, fmt.Sprintf("unknown edge kind %q", e.Kind))
		}
		if e.Via != "" {
			via, ok := byID[e.Via]
			switch {
			case !ok:
				add(p+".via", "", e.ID, fmt.Sprintf("via references unknown node %q", e.Via))
			case via.Kind != KindProxy:
				add(p+".via", e.Via, e.ID, fmt.Sprintf("via must reference a proxy node, %q is %s", e.Via, via.Kind))
			}
		}
		if !okFrom || !okTo || !e.Kind.Valid() {
			continue
		}
		// A proxy may stand in for either end of any edge: traffic is routed
		// through it to whatever sits behind it.
		switch e.Kind {
		case EdgePublishes:
			if to.Kind != KindBroker && to.Kind != KindProxy {
				add(p+".to", to.ID, e.ID, "publishes edges must point at a broker (or a proxy in front of one)")
			}
			if e.Topic == "" {
				add(p+".topic", "", e.ID, "publishes edges require a topic")
			}
		case EdgeSubscribes:
			if from.Kind != KindBroker && from.Kind != KindProxy {
				add(p+".from", from.ID, e.ID, "subscribes edges must originate at a broker (or a proxy in front of one)")
			}
			if e.Topic == "" {
				add(p+".topic", "", e.ID, "subscribes edges require a topic")
			}
		case EdgeCalls:
			if to.Kind == KindDB || to.Kind == KindBroker {
				add(p+".to", to.ID, e.ID, fmt.Sprintf("calls edges cannot target a %s; use `uses`", to.Kind))
			}
			if e.Topic != "" {
				warn(p+".topic", "", e.ID, "topic is ignored on calls edges")
			}
		case EdgeUses:
			if to.Kind != KindDB && to.Kind != KindBroker && to.Kind != KindProxy {
				add(p+".to", to.ID, e.ID, "uses edges must target a db or broker (or a proxy in front of one)")
			}
		}
		if e.TimeoutMs < 0 || e.Retries < 0 {
			add(p, "", e.ID, "timeout_ms and retries cannot be negative")
		}
		if e.Kind.Messaging() && e.Topic != "" {
			if _, ok := t.BrokerBehind(e); !ok {
				add(p, "", e.ID, fmt.Sprintf("topic %q is routed through a proxy that is not connected to a broker for it", e.Topic))
			}
		}
	}

	// Every declared topic should have both a producer and a consumer,
	// otherwise the saga can never complete. Edges are attributed to the
	// broker behind any proxy hops.
	producers := map[string]bool{}
	consumers := map[string]bool{}
	for _, e := range t.Edges {
		broker, ok := t.BrokerBehind(e)
		if !ok {
			continue
		}
		switch e.Kind {
		case EdgePublishes:
			producers[broker+"/"+e.Topic] = true
		case EdgeSubscribes:
			consumers[broker+"/"+e.Topic] = true
		}
	}
	for k := range producers {
		if !consumers[k] {
			warn("edges", "", "", fmt.Sprintf("topic %s has a producer but no subscriber", k))
		}
	}
	for k := range consumers {
		if !producers[k] {
			warn("edges", "", "", fmt.Sprintf("topic %s has a subscriber but no producer", k))
		}
	}
	return errs
}
