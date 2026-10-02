package spec

import (
	"testing"

	"symphony/blocks/common/faults"
)

// Every fault kind a block understands can be injected through the engine.
func TestBlockFaultKindsAreFaultKinds(t *testing.T) {
	for _, k := range faults.Kinds {
		if !FaultKind(k).Valid() {
			t.Errorf("the blocks' %q is not a valid FaultKind", k)
		}
	}
	seen := map[FaultKind]bool{}
	for _, k := range AllFaultKinds {
		if seen[k] {
			t.Errorf("%q is listed twice", k)
		}
		seen[k] = true
		if k.Chaos() != (k.ChaosOp() != "") {
			t.Errorf("%q: Chaos and ChaosOp disagree", k)
		}
	}
	if !FaultReorder.Valid() || FaultKind("nope").Valid() {
		t.Error("Valid is wrong about reorder or about an unknown kind")
	}
}

func TestNeedsRevert(t *testing.T) {
	for _, tc := range []struct {
		f    FaultSpec
		want bool
	}{
		{FaultSpec{Kind: FaultKill}, true},
		{FaultSpec{Kind: FaultPause}, true},
		{FaultSpec{Kind: FaultCrash}, true},
		{FaultSpec{Kind: FaultPartition}, true},
		{FaultSpec{Kind: FaultPartition, Params: map[string]any{"peers": []any{"a"}}}, false},
		{FaultSpec{Kind: FaultLatency}, false},
		{FaultSpec{Kind: FaultCPU}, false},
	} {
		if got := tc.f.NeedsRevert(); got != tc.want {
			t.Errorf("%s %v: NeedsRevert %v, want %v", tc.f.Kind, tc.f.Params, got, tc.want)
		}
	}
}
