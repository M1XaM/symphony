package spec

import (
	"strings"
	"testing"
)

func errorsOf(t Topology) []string {
	var out []string
	for _, f := range t.Normalized().Validate() {
		if f.Severity == SeverityError {
			out = append(out, f.Error())
		}
	}
	return out
}

func TestPresetsValidate(t *testing.T) {
	for _, p := range Presets() {
		if errs := errorsOf(p); len(errs) > 0 {
			t.Errorf("%s: %v", p.Name, errs)
		}
	}
}

func TestValidateIDs(t *testing.T) {
	for id, want := range map[string]bool{
		"payment": true, "e-gw-orch": true, "Order_2.v1": true,
		"": false, "-lead": false, "has space": false, "a/b": false, "{x}": false,
		strings.Repeat("a", 64): false,
	} {
		if got := ValidID(id); got != want {
			t.Errorf("ValidID(%q) = %v, want %v", id, got, want)
		}
	}
	topo := Topology{Version: CurrentVersion, Name: "t", Nodes: []Node{
		NewNode("gateway", KindInfra, Position{}), NewNode("bad id", KindService, Position{}),
	}, Edges: []Edge{{ID: "e 1", From: "gateway", To: "bad id", Kind: EdgeCalls}}}
	errs := strings.Join(errorsOf(topo), "\n")
	for _, want := range []string{`node id "bad id"`, `edge id "e 1"`} {
		if !strings.Contains(errs, want) {
			t.Errorf("missing %q in:\n%s", want, errs)
		}
	}
}

func TestValidateRejectsNonASCIIName(t *testing.T) {
	for name, want := range map[string]bool{"Fan-out saga (v2) #1!": true, "Сага": false, "saga\tone": false, "café": false} {
		topo := Topology{Version: CurrentVersion, Name: name, Nodes: []Node{NewNode("gateway", KindInfra, Position{})}}
		if got := !HasErrors(topo.Validate()); got != want {
			t.Errorf("name %q: valid = %v, want %v", name, got, want)
		}
	}
}

// A node with replicas names its containers "<id>-<n>", which is also how
// the canvas names the second node of a kind.
func TestValidateReplicaNameClash(t *testing.T) {
	a := NewNode("service", KindService, Position{})
	a.Replicas = 2
	topo := Topology{Version: CurrentVersion, Name: "t", Nodes: []Node{a, NewNode("service-1", KindService, Position{})}}
	errs := strings.Join(errorsOf(topo), "\n")
	if !strings.Contains(errs, `container name "service-1"`) {
		t.Fatalf("clash not reported:\n%s", errs)
	}
	topo.Nodes[0].Replicas = 1
	if errs := errorsOf(topo); len(errs) > 0 {
		t.Fatalf("unexpected: %v", errs)
	}
}

func TestValidateWarnsAboutASecondGateway(t *testing.T) {
	topo := Topology{Version: CurrentVersion, Name: "t", Nodes: []Node{
		NewNode("gateway", KindInfra, Position{}), NewNode("gateway-2", KindInfra, Position{}),
	}}.Normalized()
	var warnings []string
	for _, f := range topo.Validate() {
		if f.Severity != SeverityWarning {
			t.Fatalf("unexpected error: %v", f)
		}
		warnings = append(warnings, f.Message)
	}
	if got := strings.Join(warnings, "\n"); !strings.Contains(got, "more than one gateway") {
		t.Fatalf("warnings: %s", got)
	}
}
