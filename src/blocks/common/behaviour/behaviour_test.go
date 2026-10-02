package behaviour

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"symphony/blocks/common/links"
	"symphony/blocks/common/wire"
)

// env records what a program does and fails the edges it is told to.
type env struct {
	sent    []string
	fail    map[string]bool
	outcome string
}

func (e *env) Node() string                                 { return "n" }
func (e *env) Rand() float64                                { return 0.5 }
func (e *env) ConfigString(string) string                   { return "cfg" }
func (e *env) Work(context.Context, float64, float64) error { return nil }
func (e *env) ReportOutcome(_, outcome string)              { e.outcome = outcome }
func (e *env) Send(_ context.Context, edge string, _ wire.Meta, req wire.Request, _ time.Duration) (wire.Response, error) {
	e.sent = append(e.sent, edge+":"+req.Step)
	if e.fail[edge] {
		return wire.Response{}, errors.New("down")
	}
	return wire.Response{Node: "peer"}, nil
}

func in(kind string) Input {
	return Input{Link: links.Link{Edge: "in", Kind: kind}, Request: wire.Request{Saga: "s1", Step: "go"}}
}

func orchestrator() links.Set {
	return links.Set{
		In: []links.Link{{Edge: "in", Kind: "calls"}},
		Out: []links.Link{
			{Edge: "pay", Kind: "calls"}, {Edge: "inv", Kind: "calls"}, {Edge: "db", Kind: "uses"},
		},
	}
}

func TestDefaultOrchestratorCallsInOrder(t *testing.T) {
	prog := Default(orchestrator())
	h, ok := prog.Match(links.Link{Edge: "in", Kind: "calls"})
	if !ok {
		t.Fatal("no handler for the inbound calls edge")
	}
	e := &env{}
	res := Run(context.Background(), e, h, in("calls"))
	if res.Err != nil || res.Status != 200 || res.Outcome != "ok" {
		t.Fatalf("result %+v", res)
	}
	if got := strings.Join(e.sent, " "); got != "db:put pay:reserve inv:reserve db:put" {
		t.Fatalf("sent %q", got)
	}
}

// When a step fails the calls made so far are compensated in reverse.
func TestDefaultOrchestratorCompensates(t *testing.T) {
	h, _ := Default(orchestrator()).Match(links.Link{Edge: "in", Kind: "calls"})
	e := &env{fail: map[string]bool{"inv": true}}
	res := Run(context.Background(), e, h, in("calls"))
	if res.Err == nil || res.Status != 500 || res.Outcome != "compensated" {
		t.Fatalf("result %+v", res)
	}
	if got := strings.Join(e.sent, " "); got != "db:put pay:reserve inv:reserve inv:compensate pay:compensate db:put" {
		t.Fatalf("sent %q", got)
	}
}

func TestDefaultChoreographyStepAndSink(t *testing.T) {
	step := links.Set{In: []links.Link{{Edge: "in", Kind: "subscribes"}}, Out: []links.Link{{Edge: "pub", Kind: "publishes"}}}
	h, _ := Default(step).Match(links.Link{Edge: "in", Kind: "subscribes"})
	e := &env{}
	if res := Run(context.Background(), e, h, in("subscribes")); res.Err != nil || e.outcome != "" {
		t.Fatalf("step: %+v, outcome %q", res, e.outcome)
	}
	if got := strings.Join(e.sent, " "); got != "pub:n.done" {
		t.Fatalf("step sent %q", got)
	}

	sink := links.Set{In: []links.Link{{Edge: "in", Kind: "subscribes"}}}
	h, _ = Default(sink).Match(links.Link{Edge: "in", Kind: "subscribes"})
	e = &env{}
	if res := Run(context.Background(), e, h, in("subscribes")); res.Err != nil || e.outcome != "ok" {
		t.Fatalf("sink: %+v, outcome %q", res, e.outcome)
	}
}

func TestParseRejectsBadPrograms(t *testing.T) {
	for name, src := range map[string]string{
		"not json":          `{`,
		"no selector":       `{"handlers":[{"on":{},"steps":[]}]}`,
		"two step kinds":    `{"handlers":[{"on":{"kind":"*"},"steps":[{"sleep":{"ms":1},"fail":{"rate":1}}]}]}`,
		"call without edge": `{"handlers":[{"on":{"kind":"*"},"steps":[{"call":{}}]}]}`,
		"bad nested step":   `{"handlers":[{"on":{"kind":"*"},"steps":[{"parallel":[{"use":{"edge":"db"}}]}]}]}`,
	} {
		if _, err := Parse([]byte(src)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	p, err := Parse([]byte(`{"handlers":[{"on":{"edge":"e1"},"steps":[{"fail":{"rate":1,"message":"no stock"}}],"on_error":[{"reply":{"status":409,"outcome":"failed"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	h, ok := p.Match(links.Link{Edge: "e1", Kind: "calls"})
	if !ok {
		t.Fatal("handler not matched by edge")
	}
	// env.Rand is 0.5, below the rate of 1: the step fails
	res := Run(context.Background(), &env{}, h, Input{Link: links.Link{Edge: "e1"}})
	if res.Err == nil || res.Err.Error() != "no stock" || res.Status != 409 || res.Outcome != "failed" {
		t.Fatalf("result %+v", res)
	}
}
