// Package tests runs predefined stress scenarios against an applied
// topology, collects the series the collector already produces, evaluates
// assertions per phase and stores the run so it can be compared with the
// same scenario on another template.
package tests

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"sort"
	"strings"
)

//go:embed scenarios/*.json
var scenarioFS embed.FS

// Requires says what a template must contain to run a scenario.
type Requires struct {
	Kinds       []string `json:"kinds,omitempty"`
	NeedsProxy  bool     `json:"needs_proxy,omitempty"`
	NeedsBroker bool     `json:"needs_broker,omitempty"`
}

// Step is one timeline entry. Times and args may reference parameters as
// "{name}" (a string that is exactly a parameter reference takes the
// parameter's type; inside longer strings it is formatted).
//
// Ops: phase, load.set, load.stop, fault.set, fault.clear, control,
// config.set, resources.set, restart, replicas.set, teardown, apply, end.
// Targets: node (id), kind (first node of that kind), edge (id, or
// "proxied" for the first edge leaving a proxy / the first calls edge).
type Step struct {
	AtS   any            `json:"at_s"`
	Do    string         `json:"do"`
	Phase string         `json:"phase,omitempty"`
	Node  string         `json:"node,omitempty"`
	Kind  string         `json:"kind,omitempty"`
	Edge  string         `json:"edge,omitempty"`
	Args  map[string]any `json:"args,omitempty"`
	Note  string         `json:"note,omitempty"`
}

// Assertion checks an aggregate of a series over a phase.
type Assertion struct {
	Metric string  `json:"metric"` // series name, optionally with .mean/.max/.min/.last (mean default)
	Op     string  `json:"op"`     // <=, <, >=, >, ==
	Value  float64 `json:"value"`
	Phase  string  `json:"phase,omitempty"` // "" = whole run
	Note   string  `json:"note,omitempty"`
}

// Scenario is one predefined test.
type Scenario struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Tags        []string       `json:"tags,omitempty"`
	Requires    Requires       `json:"requires"`
	Params      map[string]any `json:"params"`
	Timeline    []Step         `json:"timeline"`
	Collect     []string       `json:"collect,omitempty"`
	Assert      []Assertion    `json:"assert,omitempty"`
	// Reveals says what the scenario is meant to show when compared across templates.
	Reveals string `json:"reveals,omitempty"`
}

// Catalogue returns the predefined scenarios, sorted by id; nil if the
// embedded files cannot be read (a Runner reports why).
func Catalogue() []Scenario {
	all, _ := loadScenarios()
	return all
}

// loadScenarios reads the embedded catalogue, sorted by id.
func loadScenarios() ([]Scenario, error) {
	var out []Scenario
	err := fs.WalkDir(scenarioFS, "scenarios", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := scenarioFS.ReadFile(p)
		if err != nil {
			return err
		}
		var s Scenario
		if err := json.Unmarshal(b, &s); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if s.Params == nil {
			s.Params = map[string]any{}
		}
		out = append(out, s)
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, err
}

// resolve substitutes parameter references in a value.
func resolve(v any, params map[string]any) any {
	switch x := v.(type) {
	case string:
		if strings.HasPrefix(x, "{") && strings.HasSuffix(x, "}") && !strings.Contains(x[1:len(x)-1], "{") {
			if p, ok := params[x[1:len(x)-1]]; ok {
				return p
			}
			return x
		}
		out := x
		for k, p := range params {
			out = strings.ReplaceAll(out, "{"+k+"}", fmt.Sprint(p))
		}
		return out
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, val := range x {
			m[k] = resolve(val, params)
		}
		return m
	case []any:
		l := make([]any, len(x))
		for i, val := range x {
			l[i] = resolve(val, params)
		}
		return l
	}
	return v
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		var f float64
		if _, err := fmt.Sscan(n, &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

// duration is the timeline's last time.
func (s Scenario) duration(params map[string]any) float64 {
	end := 0.0
	for _, st := range s.Timeline {
		if t, ok := toFloat(resolve(st.AtS, params)); ok && t > end {
			end = t
		}
	}
	return math.Max(end, 1)
}
