package metrics

import (
	"testing"

	bmetrics "symphony/blocks/common/metrics"
	"symphony/engine/blockclient"
	"symphony/engine/core"
	"symphony/engine/events"
)

func TestLevelThresholds(t *testing.T) {
	for _, tc := range []struct {
		rate float64
		want events.LinkLevel
	}{
		{0, events.LinkOK},
		{WarnThreshold - 0.001, events.LinkOK},
		{WarnThreshold, events.LinkWarn},
		{CritThreshold - 0.001, events.LinkWarn},
		{CritThreshold, events.LinkCrit},
		{1, events.LinkCrit},
	} {
		if got := Level(tc.rate); got != tc.want {
			t.Errorf("Level(%v) = %v, want %v", tc.rate, got, tc.want)
		}
	}
}

// A long session applies and tears down repeatedly; the per-client deltas of
// containers that are gone must not pile up.
func TestPruneForgetsDeadClientsAndDeployments(t *testing.T) {
	c := NewCollector(events.NewBus(), 500, nil)
	gone := blockclient.New("127.0.0.1:1", "")
	live := blockclient.New("127.0.0.1:2", "")
	c.prev[gone] = scrape{}
	c.prev[live] = scrape{}
	c.lastLink["gone"] = map[string]events.LinkHealthEntry{}
	c.lastLink["live"] = map[string]events.LinkHealthEntry{}

	c.prune([]core.DeploymentSnapshot{{
		Name:  "live",
		Nodes: map[string]core.NodeSnapshot{"a": {Clients: []*blockclient.Client{live}}},
	}})

	if _, ok := c.prev[gone]; ok {
		t.Error("the dead client's scrape is still cached")
	}
	if _, ok := c.prev[live]; !ok {
		t.Error("the live client's scrape was dropped")
	}
	if _, ok := c.lastLink["gone"]; ok {
		t.Error("the torn-down deployment's link memory is still cached")
	}
	if _, ok := c.lastLink["live"]; !ok {
		t.Error("the live deployment's link memory was dropped")
	}
}

// Counters are read as the increase since the previous scrape, and a block
// that restarted counts from zero again rather than going negative.
func TestIncreaseSurvivesARestart(t *testing.T) {
	ctr := bmetrics.CounterSnapshot{Name: "saga_total", Labels: bmetrics.Labels{"outcome": "ok"}, Value: 10}
	prev := scrape{counters: map[string]int64{counterKey(ctr): 4}}
	if got := prev.increase(ctr); got != 6 {
		t.Fatalf("increase = %v, want 6", got)
	}
	ctr.Value = 3
	if got := prev.increase(ctr); got != 3 {
		t.Fatalf("after a restart increase = %v, want 3", got)
	}
}
