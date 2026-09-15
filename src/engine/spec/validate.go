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

	edgeIDs := map[string]bool{}
	for i, e := range t.Edges {
		p := fmt.Sprintf("edges[%d]", i)
		if e.ID == "" {
			add(p+".id", "", "", "edge id is required")
		} else if edgeIDs[e.ID] {
			add(p+".id", "", e.ID, fmt.Sprintf("duplicate edge id %q", e.ID))
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
