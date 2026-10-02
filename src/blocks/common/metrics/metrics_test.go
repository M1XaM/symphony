package metrics

import (
	"testing"
	"time"
)

// Percentiles describe the window that ends now, not everything since the
// block started: once a slow spell is over it leaves the numbers.
func TestHistogramPercentilesAreWindowed(t *testing.T) {
	now := time.Unix(1000, 0)
	h := NewRegistry().Histogram("lat", nil)
	h.now = func() time.Time { return now }

	for i := 0; i < 100; i++ {
		h.Observe(900) // a slow spell
	}
	now = now.Add(10 * time.Second)
	for i := 0; i < 10; i++ {
		h.Observe(20)
	}
	s := h.snapshot(5 * time.Second)
	if s.Recent != 10 || s.P99 != 20 || s.Max != 20 {
		t.Fatalf("window of 5s: recent %d p99 %v max %v, want 10 / 20 / 20", s.Recent, s.P99, s.Max)
	}
	if s.Count != 110 || s.Buckets["1000"] != 110 || s.Buckets["20"] != 10 {
		t.Fatalf("totals must still cover everything: count %d buckets %v", s.Count, s.Buckets)
	}
	if s = h.snapshot(time.Minute); s.Recent != 110 || s.P99 != 900 {
		t.Fatalf("window of 1m: recent %d p99 %v, want 110 / 900", s.Recent, s.P99)
	}
	now = now.Add(time.Minute)
	if s = h.snapshot(5 * time.Second); s.Recent != 0 || s.P50 != 0 || s.P99 != 0 {
		t.Fatalf("an idle window must be empty: %+v", s)
	}
}

func TestHistogramRingKeepsTheNewestSamples(t *testing.T) {
	h := NewRegistry().Histogram("lat", nil)
	for i := 0; i < ringSize; i++ {
		h.Observe(1)
	}
	for i := 0; i < ringSize; i++ {
		h.Observe(7)
	}
	if s := h.snapshot(time.Minute); s.Recent != ringSize || s.P50 != 7 || s.Count != 2*ringSize {
		t.Fatalf("recent %d p50 %v count %d", s.Recent, s.P50, s.Count)
	}
}
