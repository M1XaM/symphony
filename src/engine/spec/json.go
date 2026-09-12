package spec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// CurrentVersion is the topology schema version this build writes.
const CurrentVersion = 1

// Decode reads a topology from JSON and normalises it.
func Decode(r io.Reader) (Topology, error) {
	var t Topology
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		return Topology{}, fmt.Errorf("spec: decode topology: %w", err)
	}
	return t.Normalized(), nil
}

// FromJSON is Decode over a byte slice.
func FromJSON(b []byte) (Topology, error) {
	return Decode(bytes.NewReader(b))
}

// Encode writes the topology as indented JSON.
func Encode(w io.Writer, t Topology) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(t.Normalized())
}

// ToJSON is Encode into a byte slice.
func ToJSON(t Topology) ([]byte, error) {
	var buf bytes.Buffer
	if err := Encode(&buf, t); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Normalized fills defaults and canonicalises values so that two topologies
// that mean the same thing compare equal in the planner: nil maps become
// empty, replicas default to 1, and config values are round-tripped through
// JSON so numbers are always float64 regardless of whether the topology was
// built in Go or arrived from the frontend.
func (t Topology) Normalized() Topology {
	out := t.Clone()
	if out.Version == 0 {
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
		n.Config.Hot = canonMap(n.Config.Hot)
		n.Config.Cold = canonMap(n.Config.Cold)
	}
	return out
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
