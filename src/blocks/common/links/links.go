// Package links describes a block's connections, handed over by the engine in
// the SYMPHONY_LINKS environment variable. One inbound link is one endpoint
// on the data port; one outbound link is one client with its targets.
package links

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// Link is one edge of the topology as seen from this node.
type Link struct {
	Edge  string `json:"edge"`
	Kind  string `json:"kind"` // calls | uses | publishes | subscribes
	From  string `json:"from"` // node id
	To    string `json:"to"`   // node id
	Topic string `json:"topic,omitempty"`
	// Targets are the data-port base URLs of the peer's replicas; only set
	// on outbound links.
	Targets []string `json:"targets,omitempty"`
	// TimeoutMs and Retries override the node's timeout_ms / retry_max on
	// this link when non-zero.
	TimeoutMs int `json:"timeout_ms,omitempty"`
	Retries   int `json:"retries,omitempty"`
}

// Peer is the node at the other end of the link from this node's point of view.
func (l Link) Peer(self string) string {
	if l.From == self {
		return l.To
	}
	return l.From
}

// Pair joins an inbound link to the outbound link that continues it. Only
// proxies get pairs; the engine computes them from the topology.
type Pair struct {
	In  string `json:"in"`
	Out string `json:"out"`
}

// Set is everything the engine tells a node about its wiring.
type Set struct {
	In    []Link `json:"in"`
	Out   []Link `json:"out"`
	Pairs []Pair `json:"pairs,omitempty"`
	// Gateway is the admin URL of the deployment's gateway (if any), used by
	// sinks to report a saga's outcome.
	Gateway string `json:"gateway,omitempty"`
}

// EnvVar is where the engine puts the JSON.
const EnvVar = "SYMPHONY_LINKS"

// FromEnv parses SYMPHONY_LINKS. An unset variable yields an empty set, so a
// block started by hand still comes up.
func FromEnv() (Set, error) {
	raw := os.Getenv(EnvVar)
	if raw == "" {
		return Set{}, nil
	}
	return Parse([]byte(raw))
}

// Parse decodes and validates a link set.
func Parse(b []byte) (Set, error) {
	var s Set
	if err := json.Unmarshal(b, &s); err != nil {
		return Set{}, fmt.Errorf("links: %w", err)
	}
	seen := map[string]bool{}
	for _, l := range append(append([]Link{}, s.In...), s.Out...) {
		if l.Edge == "" {
			return Set{}, errors.New("links: link without edge id")
		}
	}
	for _, l := range s.In {
		if seen["in:"+l.Edge] {
			return Set{}, fmt.Errorf("links: duplicate inbound link %q", l.Edge)
		}
		seen["in:"+l.Edge] = true
	}
	for _, l := range s.Out {
		if seen["out:"+l.Edge] {
			return Set{}, fmt.Errorf("links: duplicate outbound link %q", l.Edge)
		}
		seen["out:"+l.Edge] = true
		if len(l.Targets) == 0 {
			return Set{}, fmt.Errorf("links: outbound link %q has no targets", l.Edge)
		}
	}
	for _, p := range s.Pairs {
		if !seen["in:"+p.In] || !seen["out:"+p.Out] {
			return Set{}, fmt.Errorf("links: pair %s -> %s references unknown links", p.In, p.Out)
		}
	}
	return s, nil
}

// Outbound looks an outbound link up by edge id.
func (s Set) Outbound(edge string) (Link, bool) {
	for _, l := range s.Out {
		if l.Edge == edge {
			return l, true
		}
	}
	return Link{}, false
}

// OutboundOfKind returns the outbound links of one kind, in declaration order.
func (s Set) OutboundOfKind(kind string) []Link {
	var out []Link
	for _, l := range s.Out {
		if l.Kind == kind {
			out = append(out, l)
		}
	}
	return out
}

// InboundOfKind returns the inbound links of one kind, in declaration order.
func (s Set) InboundOfKind(kind string) []Link {
	var out []Link
	for _, l := range s.In {
		if l.Kind == kind {
			out = append(out, l)
		}
	}
	return out
}
