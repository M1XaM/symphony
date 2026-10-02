package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"symphony/blocks/common/faults"
	"symphony/blocks/common/links"
	"symphony/blocks/common/metrics"
	"symphony/blocks/common/wire"
)

// The breaker opens after the threshold, refuses calls while open, and
// Reset closes it at once.
func TestBreakerOpensAndResets(t *testing.T) {
	var fail atomic.Bool
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"node":"peer"}`))
	}))
	defer srv.Close()

	settings := func() Settings {
		return Settings{Timeout: time.Second, Retries: 0, BreakerThreshold: 3, BreakerOpen: time.Minute}
	}
	c := New("self", links.Link{Edge: "e1", Kind: "calls", From: "self", To: "peer", Targets: []string{srv.URL}},
		settings, faults.NewStore(nil), metrics.NewRegistry(), DefaultTransport())
	call := func() Result { return c.Call(context.Background(), wire.Meta{Saga: "s"}, wire.Request{Saga: "s"}) }

	if res := call(); res.Outcome != OutcomeOK {
		t.Fatalf("healthy peer: %+v", res)
	}
	fail.Store(true)
	for i := 0; i < 3; i++ {
		if res := call(); res.Outcome != OutcomeError {
			t.Fatalf("failing peer, call %d: %+v", i, res)
		}
	}
	if c.TargetsUp() != 0 {
		t.Fatal("the breaker did not open after the threshold")
	}
	before := hits.Load()
	if res := call(); res.Outcome != OutcomeRefused || hits.Load() != before {
		t.Fatalf("an open breaker must refuse without calling: %+v", res)
	}

	fail.Store(false)
	c.Reset()
	if c.TargetsUp() != 1 {
		t.Fatal("Reset did not close the breaker")
	}
	if res := call(); res.Outcome != OutcomeOK {
		t.Fatalf("after Reset: %+v", res)
	}
}
