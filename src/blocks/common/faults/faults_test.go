package faults

import (
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDecideScopesAndComposes(t *testing.T) {
	s := NewStore(nil)
	rng := rand.New(rand.NewSource(1))
	if _, err := s.Set("nope", Target{}, nil, 0); err == nil {
		t.Fatal("an unknown kind was accepted")
	}
	if _, err := s.Set(Drop, Target{Direction: "sideways"}, nil, 0); err == nil {
		t.Fatal("a bad direction was accepted")
	}
	// scoped to one edge, outbound
	if _, err := s.Set(Latency, Target{Edge: "e1", Direction: "out"}, map[string]any{"ms": 100.0}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set(Error, Target{Edge: "e1"}, map[string]any{"status": 502.0}, 0); err != nil {
		t.Fatal(err)
	}
	d := s.Decide(Scope{Edge: "e1", Direction: "out"}, rng)
	if d.Delay != 100*time.Millisecond || d.ErrorStatus != 502 {
		t.Fatalf("e1 out: %+v", d)
	}
	if d = s.Decide(Scope{Edge: "e1", Direction: "in"}, rng); d.Delay != 0 || d.ErrorStatus != 502 {
		t.Fatalf("e1 in: %+v", d)
	}
	if d = s.Decide(Scope{Edge: "e2", Direction: "out"}, rng); d != (Decision{}) {
		t.Fatalf("another edge is affected: %+v", d)
	}
}

func TestDecideRateAndPartitionPeers(t *testing.T) {
	s := NewStore(nil)
	rng := rand.New(rand.NewSource(1))
	if _, err := s.Set(Drop, Target{}, map[string]any{"rate": 0.3}, 0); err != nil {
		t.Fatal(err)
	}
	dropped := 0
	for i := 0; i < 10000; i++ {
		if s.Decide(Scope{Edge: "e"}, rng).Dropped {
			dropped++
		}
	}
	if dropped < 2700 || dropped > 3300 {
		t.Fatalf("rate 0.3 dropped %d of 10000", dropped)
	}
	s.ClearAll()
	if _, err := s.Set(Partition, Target{}, map[string]any{"peers": []any{"payment"}}, 0); err != nil {
		t.Fatal(err)
	}
	if !s.Decide(Scope{Peer: "payment"}, rng).Partitioned || s.Decide(Scope{Peer: "inventory"}, rng).Partitioned {
		t.Fatal("a selective partition must cut the named peers only")
	}
}

func TestFaultsExpireAndClear(t *testing.T) {
	// the expiry is announced from the timer's goroutine
	var mu sync.Mutex
	var events []string
	s := NewStore(func(ev string, _ Fault) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	})
	f, _ := s.Set(Drop, Target{}, nil, 30*time.Millisecond)
	keep, _ := s.Set(Latency, Target{}, nil, 0)
	if f.RemainingMs() <= 0 || keep.RemainingMs() != -1 {
		t.Fatalf("remaining: %d, %d", f.RemainingMs(), keep.RemainingMs())
	}
	time.Sleep(80 * time.Millisecond)
	if list := s.List(); len(list) != 1 || list[0].ID != keep.ID {
		t.Fatalf("after expiry: %+v", list)
	}
	if !s.Clear(keep.ID) || s.Clear(keep.ID) {
		t.Fatal("Clear must report whether the fault was there")
	}
	if len(s.List()) != 0 {
		t.Fatal("faults left")
	}
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(events, " "); got != "fault.set fault.set fault.expired fault.cleared" {
		t.Fatalf("announced %q", got)
	}
}
