package spec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// CurrentVersion is the topology schema version this build writes.
//
//   - v1: original schema, third-party images (nginx, kafka, postgres, toxiproxy)
//   - v2: nodes run Symphony's own block images (symphony/<block>:dev), carry
//     resources and an optional behaviour program; edges carry timeouts
const CurrentVersion = 2

// DefaultImage is the block image for a node kind.
func DefaultImage(k NodeKind) string {
	switch k {
	case KindService:
		return "symphony/service:dev"
	case KindBroker:
		return "symphony/broker:dev"
	case KindDB:
		return "symphony/database:dev"
	case KindProxy:
		return "symphony/proxy:dev"
	case KindInfra:
		return "symphony/gateway:dev"
	}
	return ""
}

// IsBlockImage reports whether an image is one of Symphony's own blocks.
func IsBlockImage(image string) bool {
	return strings.HasPrefix(image, "symphony/")
}

// DefaultResources are the limits presets and new nodes start with.
func DefaultResources(k NodeKind) Resources {
	switch k {
	case KindBroker, KindDB:
		return Resources{CPUs: 1, MemoryMB: 512}
	case KindProxy:
		return Resources{CPUs: 0.25, MemoryMB: 128}
	case KindInfra:
		return Resources{CPUs: 0.5, MemoryMB: 128}
	}
	return Resources{CPUs: 0.5, MemoryMB: 256}
}

// Normalized fills defaults and canonicalises values so that two topologies
// that mean the same thing compare equal in the planner: nil maps become
// empty, replicas default to 1, and config values are round-tripped through
// JSON so numbers are always float64 regardless of whether the topology was
// built in Go or arrived from the frontend.
func (t Topology) Normalized() Topology {
	out := t.Clone()
	migrate := out.Version != 0 && out.Version < CurrentVersion
	if out.Version == 0 || out.Version < CurrentVersion {
		out.Version = CurrentVersion
	}
	if out.Nodes == nil {
		out.Nodes = []Node{}
	}
	if out.Edges == nil {
		out.Edges = []Edge{}
	}
	for i := range out.Nodes {
		n := &out.Nodes[i]
		if n.Replicas <= 0 {
			n.Replicas = 1
		}
		if migrate || n.Image == "" {
			// v1 topologies named third-party images the runtime never ran;
			// a v2 node runs the block for its kind
			if n.Image == "" || !IsBlockImage(n.Image) {
				n.Image = DefaultImage(n.Kind)
			}
			if n.Resources == (Resources{}) {
				n.Resources = DefaultResources(n.Kind)
			}
		}
		n.Config.Hot = canonMap(n.Config.Hot)
		n.Config.Cold = canonMap(n.Config.Cold)
		n.Behaviour = canonRaw(n.Behaviour)
	}
	return out
}

// canonRaw compacts a JSON document; empty or "null" becomes nil.
func canonRaw(b json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, trimmed); err != nil {
		return trimmed
	}
	return buf.Bytes()
}

func canonMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return copyMap(m)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil || out == nil {
		return copyMap(m)
	}
	return out
}

// CanonValue renders a config value as canonical JSON, used for equality.
func CanonValue(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}
