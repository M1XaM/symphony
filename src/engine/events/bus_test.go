package events

import (
	"sync"
	"testing"
)

func TestSubscribeAndUnsubscribe(t *testing.T) {
	b := NewBus()
	var got []Type
	stop := b.Subscribe(func(ev Event) { got = append(got, ev.Type) })
	b.Publish(MetricsTick, nil)
	stop()
	b.Publish(MetricsTick, nil)
	if len(got) != 1 {
		t.Fatalf("handler ran %d times, want once before it was removed", len(got))
	}
}

func TestPublishIsSafeWhileSubscribing(t *testing.T) {
	b := NewBus()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stop := b.Subscribe(func(Event) {})
			for j := 0; j < 200; j++ {
				b.Publish(HostStats, j)
			}
			stop()
		}()
	}
	wg.Wait()
}

func TestLogHistoryKeepsTheLastCapLines(t *testing.T) {
	b := NewBus()
	total := logHistoryCap + logHistoryTrim*3
	for i := 0; i < total; i++ {
		b.Log("info", "test", string(rune('a'+i%26)))
	}
	h := b.LogHistory()
	if len(h) != logHistoryCap {
		t.Fatalf("history holds %d lines, want the cap %d", len(h), logHistoryCap)
	}
	// oldest first, and the newest line is the one just logged
	want := string(rune('a' + (total-1)%26))
	if h[len(h)-1].Message != want {
		t.Fatalf("last line = %q, want %q", h[len(h)-1].Message, want)
	}
	first := string(rune('a' + (total-logHistoryCap)%26))
	if h[0].Message != first {
		t.Fatalf("first line = %q, want %q", h[0].Message, first)
	}
}

func BenchmarkPublish(b *testing.B) {
	bus := NewBus()
	defer bus.Subscribe(func(Event) {})()
	p := MetricsPayload{Deployment: "bench"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bus.Publish(MetricsTick, p)
	}
}

func BenchmarkLog(b *testing.B) {
	bus := NewBus()
	defer bus.Subscribe(func(Event) {})()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bus.Log("info", "bench", "a line from a container")
	}
}
