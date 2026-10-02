package control

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"symphony/blocks/common/faults"
)

func newRouter() *Router { return NewRouter(NewConfig(), NewEvents()) }

// fault.set built by the engine reaches the fault store with the scope keys
// as its target and the rest as knobs, and its result decodes to the fault.
func TestFaultSetRoundTrip(t *testing.T) {
	r := newRouter()
	cmd := FaultSet(faults.Latency, map[string]any{"edge": "e1", "direction": "out", "ms": 250.0}, 5*time.Second)
	if cmd.Target.Edge != "e1" || cmd.Target.Direction != "out" || cmd.Params["ms"] != 250.0 || cmd.Params["kind"] != "latency" || cmd.DurationMs != 5000 {
		t.Fatalf("command %+v", cmd)
	}
	if _, scoped := cmd.Params["edge"]; scoped {
		t.Fatal("a scope key was left among the knobs")
	}
	// over the wire, as the engine sees it
	raw, _ := json.Marshal(r.Execute(cmd))
	var res Result
	if err := json.Unmarshal(raw, &res); err != nil || !res.OK {
		t.Fatalf("result %s, %v", raw, err)
	}
	var f faults.Fault
	if err := res.Decode(&f); err != nil || f.ID == "" || f.Kind != faults.Latency || f.Target.Edge != "e1" {
		t.Fatalf("decoded %+v, %v", f, err)
	}
	if res := r.Execute(FaultClear(f.ID)); !res.OK {
		t.Fatalf("fault.clear: %s", res.Error)
	}
	if len(r.Faults.List()) != 0 {
		t.Fatal("the fault is still there")
	}
}

func TestBindAndBehaviour(t *testing.T) {
	var load struct {
		RPS         float64 `json:"rps"`
		Concurrency int     `json:"concurrency"`
	}
	if err := LoadSet(map[string]any{"rps": 40.0, "concurrency": 8}).Bind(&load); err != nil || load.RPS != 40 || load.Concurrency != 8 {
		t.Fatalf("bound %+v, %v", load, err)
	}
	if err := LoadSet(map[string]any{"rps": "fast"}).Bind(&load); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("a mistyped param must be a bad request, got %v", err)
	}
	if cmd, err := Behaviour(nil); err != nil || cmd.Op != OpBehaviourReset {
		t.Fatalf("no program: %+v, %v", cmd, err)
	}
	cmd, err := Behaviour(json.RawMessage(`{"handlers":[]}`))
	if err != nil || cmd.Op != OpBehaviourSet || cmd.Params["handlers"] == nil {
		t.Fatalf("a program: %+v, %v", cmd, err)
	}
	if _, err := Behaviour(json.RawMessage(`[`)); err == nil {
		t.Fatal("a broken program was accepted")
	}
}

// A client's mistake is a 400, a handler that fails is a 500.
func TestHTTPStatus(t *testing.T) {
	r := newRouter()
	r.Config.Define("workers", 32.0)
	r.Handle("boom", func(Command) (any, error) { return nil, errors.New("disk on fire") })
	for body, want := range map[string]int{
		`{"op":"config.get"}`:                             http.StatusOK,
		`{"op":"no.such.op"}`:                             http.StatusBadRequest,
		`{"op":"config.set","params":{"nope":1}}`:         http.StatusBadRequest,
		`{"op":"config.set","params":{"workers":"many"}}`: http.StatusBadRequest,
		`{"op":"fault.set","params":{"kind":"nope"}}`:     http.StatusBadRequest,
		`{"op":"fault.clear"}`:                            http.StatusBadRequest,
		`{"op":"boom"}`:                                   http.StatusInternalServerError,
		`{`:                                               http.StatusBadRequest,
	} {
		rec := httptest.NewRecorder()
		r.HTTPHandler()(rec, httptest.NewRequest(http.MethodPost, "/control", strings.NewReader(body)))
		if rec.Code != want {
			t.Errorf("%s: status %d, want %d (%s)", body, rec.Code, want, strings.TrimSpace(rec.Body.String()))
		}
	}
}

func TestEveryCommonOpIsNamed(t *testing.T) {
	r := newRouter()
	for _, op := range []string{OpConfigGet, OpConfigSet, OpConfigReset, OpFaultSet, OpFaultClear, OpFaultList, OpStateReset, OpOps} {
		found := false
		for _, have := range r.Ops() {
			found = found || have == op
		}
		if !found {
			t.Errorf("%s is not registered", op)
		}
	}
	if !ReadOnly(OpLoadStatus) || ReadOnly(OpLoadSet) {
		t.Error("ReadOnly is wrong about load.status or load.set")
	}
}
