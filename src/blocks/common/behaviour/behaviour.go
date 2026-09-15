// Package behaviour is the small declarative language a service block runs:
// "on a request over inbound edge X, do these steps". Programs come from
// SYMPHONY_BEHAVIOUR, from /control behaviour.set, or are derived from the
// node's links when neither is given.
package behaviour

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"symphony/blocks/common/links"
	"symphony/blocks/common/wire"
)

// Program is a list of handlers; the first whose On matches the inbound link wins.
type Program struct {
	Handlers []Handler `json:"handlers"`
}

// Handler is what to do for one inbound link (or every link of a kind).
type Handler struct {
	On      On     `json:"on"`
	Steps   []Step `json:"steps"`
	OnError []Step `json:"on_error,omitempty"`
}

// On selects inbound links by edge id or by kind ("*" matches all).
type On struct {
	Edge string `json:"edge,omitempty"`
	Kind string `json:"kind,omitempty"`
}

// Step is a union; exactly one field is set.
type Step struct {
	Work     *WorkStep    `json:"work,omitempty"`
	Call     *CallStep    `json:"call,omitempty"`
	Use      *UseStep     `json:"use,omitempty"`
	Publish  *PublishStep `json:"publish,omitempty"`
	Reply    *ReplyStep   `json:"reply,omitempty"`
	Parallel []Step       `json:"parallel,omitempty"`
	Sleep    *SleepStep   `json:"sleep,omitempty"`
	Fail     *FailStep    `json:"fail,omitempty"`
	Outcome  *OutcomeStep `json:"outcome,omitempty"`
}

// WorkStep burns CPU and/or holds a worker idle, to make requests cost something.
type WorkStep struct {
	CPUMs float64 `json:"cpu_ms,omitempty"`
	IOMs  float64 `json:"io_ms,omitempty"`
}

// CallStep sends the request on over a `calls` edge.
type CallStep struct {
	Edge       string   `json:"edge"`
	Step       string   `json:"step,omitempty"`
	TimeoutMs  *float64 `json:"timeout_ms,omitempty"`
	BestEffort bool     `json:"best_effort,omitempty"` // an error does not fail the handler
}

// UseStep runs an operation against a database over a `uses` edge.
type UseStep struct {
	Edge  string `json:"edge"`
	Op    string `json:"op"` // get | put | delete | txn
	Key   string `json:"key,omitempty"`
	Value string `json:"value,omitempty"`
}

// PublishStep emits a message on a `publishes` edge (all of them when Edge is empty).
type PublishStep struct {
	Edge string `json:"edge,omitempty"`
	Step string `json:"step,omitempty"`
}

// ReplyStep ends the handler with a status and, optionally, the saga outcome.
type ReplyStep struct {
	Status  int    `json:"status,omitempty"`
	Outcome string `json:"outcome,omitempty"`
}

// SleepStep waits without holding CPU.
type SleepStep struct {
	Ms float64 `json:"ms"`
}

// FailStep fails the handler on purpose with the given probability (a
// business failure, distinct from an injected fault).
type FailStep struct {
	Rate    float64 `json:"rate"`
	Message string  `json:"message,omitempty"`
}

// OutcomeStep reports the saga's final outcome to the gateway (sinks).
type OutcomeStep struct {
	Value string `json:"value"` // ok | failed
}

// Parse decodes and validates a program.
func Parse(b []byte) (Program, error) {
	var p Program
	if err := json.Unmarshal(b, &p); err != nil {
		return Program{}, fmt.Errorf("behaviour: %w", err)
	}
	for i, h := range p.Handlers {
		if h.On.Edge == "" && h.On.Kind == "" {
			return Program{}, fmt.Errorf("behaviour: handlers[%d].on needs edge or kind", i)
		}
		if err := validate(h.Steps); err != nil {
			return Program{}, fmt.Errorf("behaviour: handlers[%d]: %w", i, err)
		}
		if err := validate(h.OnError); err != nil {
			return Program{}, fmt.Errorf("behaviour: handlers[%d].on_error: %w", i, err)
		}
	}
	return p, nil
}

func validate(steps []Step) error {
	for i, s := range steps {
		n := 0
		for _, set := range []bool{s.Work != nil, s.Call != nil, s.Use != nil, s.Publish != nil, s.Reply != nil, s.Parallel != nil, s.Sleep != nil, s.Fail != nil, s.Outcome != nil} {
			if set {
				n++
			}
		}
		if n != 1 {
			return fmt.Errorf("step %d must set exactly one of work/call/use/publish/reply/parallel/sleep/fail/outcome", i)
		}
		if s.Call != nil && s.Call.Edge == "" {
			return fmt.Errorf("step %d: call.edge required", i)
		}
		if s.Use != nil && (s.Use.Edge == "" || s.Use.Op == "") {
			return fmt.Errorf("step %d: use.edge and use.op required", i)
		}
		if s.Parallel != nil {
			if err := validate(s.Parallel); err != nil {
				return err
			}
		}
	}
	return nil
}

// Match finds the handler for an inbound link.
func (p Program) Match(l links.Link) (Handler, bool) {
	for _, h := range p.Handlers {
		if h.On.Edge != "" && h.On.Edge == l.Edge {
			return h, true
		}
	}
	for _, h := range p.Handlers {
		if h.On.Edge == "" && (h.On.Kind == "*" || h.On.Kind == l.Kind) {
			return h, true
		}
	}
	return Handler{}, false
}

// Default derives a program from the node's links:
//   - inbound calls + outbound calls/uses: orchestrator (call every outbound
//     edge in order, compensate in reverse on error)
//   - inbound calls only: participant (work, use db, reply)
//   - inbound subscribes + outbound publishes: choreography step
//   - inbound subscribes only: sink (work, report the outcome)
func Default(ls links.Set) Program {
	var p Program
	calls := ls.OutboundOfKind("calls")
	uses := ls.OutboundOfKind("uses")
	pubs := ls.OutboundOfKind("publishes")

	if in := ls.InboundOfKind("calls"); len(in) > 0 {
		h := Handler{On: On{Kind: "calls"}}
		h.Steps = append(h.Steps, Step{Work: &WorkStep{CPUMs: -1}})
		for _, u := range uses {
			h.Steps = append(h.Steps, Step{Use: &UseStep{Edge: u.Edge, Op: "put", Key: "{node}/{saga}", Value: "started"}})
		}
		if len(calls) > 0 {
			for _, c := range calls {
				h.Steps = append(h.Steps, Step{Call: &CallStep{Edge: c.Edge, Step: "reserve"}})
			}
			for i := len(calls) - 1; i >= 0; i-- {
				h.OnError = append(h.OnError, Step{Call: &CallStep{Edge: calls[i].Edge, Step: "compensate", BestEffort: true}})
			}
		}
		for _, pb := range pubs {
			h.Steps = append(h.Steps, Step{Publish: &PublishStep{Edge: pb.Edge, Step: "created"}})
		}
		for _, u := range uses {
			h.Steps = append(h.Steps, Step{Use: &UseStep{Edge: u.Edge, Op: "put", Key: "{node}/{saga}", Value: "done"}})
			h.OnError = append(h.OnError, Step{Use: &UseStep{Edge: u.Edge, Op: "put", Key: "{node}/{saga}", Value: "compensated"}})
		}
		switch {
		case len(pubs) > 0 && len(calls) == 0:
			// the saga continues asynchronously through the broker
			h.Steps = append(h.Steps, Step{Reply: &ReplyStep{Status: 200, Outcome: "pending"}})
		case len(calls) > 0:
			h.Steps = append(h.Steps, Step{Reply: &ReplyStep{Status: 200, Outcome: "ok"}})
			h.OnError = append(h.OnError, Step{Reply: &ReplyStep{Status: 500, Outcome: "compensated"}})
		default:
			h.Steps = append(h.Steps, Step{Reply: &ReplyStep{Status: 200}})
		}
		p.Handlers = append(p.Handlers, h)
	}
	if in := ls.InboundOfKind("subscribes"); len(in) > 0 {
		h := Handler{On: On{Kind: "subscribes"}}
		h.Steps = append(h.Steps, Step{Work: &WorkStep{CPUMs: -1}})
		for _, u := range uses {
			h.Steps = append(h.Steps, Step{Use: &UseStep{Edge: u.Edge, Op: "put", Key: "{node}/{saga}", Value: "{step}"}})
		}
		for _, c := range calls {
			h.Steps = append(h.Steps, Step{Call: &CallStep{Edge: c.Edge, Step: "{step}"}})
		}
		// A node that is also an entry point (inbound calls) closes the
		// loop: the event coming back to it ends the saga rather than
		// starting another round through its own publishes edge.
		if len(pubs) > 0 && len(ls.InboundOfKind("calls")) == 0 {
			for _, pb := range pubs {
				h.Steps = append(h.Steps, Step{Publish: &PublishStep{Edge: pb.Edge, Step: "{node}.done"}})
			}
		} else {
			h.Steps = append(h.Steps, Step{Outcome: &OutcomeStep{Value: "ok"}})
		}
		h.Steps = append(h.Steps, Step{Reply: &ReplyStep{Status: 200}})
		h.OnError = append(h.OnError, Step{Outcome: &OutcomeStep{Value: "failed"}}, Step{Reply: &ReplyStep{Status: 500}})
		p.Handlers = append(p.Handlers, h)
	}
	if len(p.Handlers) == 0 {
		p.Handlers = append(p.Handlers, Handler{On: On{Kind: "*"}, Steps: []Step{{Work: &WorkStep{CPUMs: -1}}, {Reply: &ReplyStep{Status: 200}}}})
	}
	return p
}

// Env is what the interpreter needs from the block.
type Env interface {
	Node() string
	// Send runs a call/use/publish over an outbound edge. timeout <= 0 means
	// the configured default.
	Send(ctx context.Context, edge string, meta wire.Meta, req wire.Request, timeout time.Duration) (wire.Response, error)
	// Work burns cpu_ms of CPU (a negative value means "the configured
	// amount") and sleeps io_ms.
	Work(ctx context.Context, cpuMs, ioMs float64) error
	Rand() float64
	// ReportOutcome tells the gateway a saga is finished (sinks).
	ReportOutcome(saga, outcome string)
	ConfigString(key string) string
}

// Input is the request being handled.
type Input struct {
	Link    links.Link
	Meta    wire.Meta
	Request wire.Request
}

// Result is what the handler produced.
type Result struct {
	Status  int
	Outcome string
	Body    map[string]any
	Err     error
}

var errStop = errors.New("reply")

// Run executes a handler for an input.
func Run(ctx context.Context, env Env, h Handler, in Input) Result {
	r := &Result{Status: 200, Body: map[string]any{}}
	err := runSteps(ctx, env, h.Steps, in, r)
	if err != nil && !errors.Is(err, errStop) {
		r.Err = err
		r.Status = 500
		if r.Outcome == "" {
			r.Outcome = "failed"
		}
		if len(h.OnError) > 0 {
			r.Body["compensation"] = "ran"
			if cerr := runSteps(ctx, env, h.OnError, in, r); cerr != nil && !errors.Is(cerr, errStop) {
				r.Body["compensation"] = "failed: " + cerr.Error()
			}
		}
	}
	return *r
}

func runSteps(ctx context.Context, env Env, steps []Step, in Input, r *Result) error {
	for _, s := range steps {
		if err := runStep(ctx, env, s, in, r); err != nil {
			return err
		}
	}
	return nil
}

func runStep(ctx context.Context, env Env, s Step, in Input, r *Result) error {
	switch {
	case s.Work != nil:
		return env.Work(ctx, s.Work.CPUMs, s.Work.IOMs)
	case s.Sleep != nil:
		select {
		case <-time.After(time.Duration(s.Sleep.Ms * float64(time.Millisecond))):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	case s.Fail != nil:
		if env.Rand() < s.Fail.Rate {
			msg := s.Fail.Message
			if msg == "" {
				msg = "business failure"
			}
			return errors.New(msg)
		}
		return nil
	case s.Call != nil:
		req := wire.Request{Saga: in.Request.Saga, Step: expand(s.Call.Step, env, in), Payload: in.Request.Payload, Trace: in.Request.Trace}
		var timeout time.Duration
		if s.Call.TimeoutMs != nil {
			timeout = time.Duration(*s.Call.TimeoutMs * float64(time.Millisecond))
		}
		resp, err := env.Send(ctx, s.Call.Edge, in.Meta, req, timeout)
		if err != nil {
			if s.Call.BestEffort {
				r.Body[s.Call.Edge] = "best-effort failed: " + err.Error()
				return nil
			}
			return fmt.Errorf("%s: %w", s.Call.Edge, err)
		}
		r.Body[s.Call.Edge] = resp.Node
		return nil
	case s.Use != nil:
		payload, _ := json.Marshal(map[string]string{"op": s.Use.Op, "key": expand(s.Use.Key, env, in), "value": expand(s.Use.Value, env, in)})
		req := wire.Request{Saga: in.Request.Saga, Step: s.Use.Op, Payload: payload, Trace: in.Request.Trace}
		if _, err := env.Send(ctx, s.Use.Edge, in.Meta, req, 0); err != nil {
			return fmt.Errorf("%s: %w", s.Use.Edge, err)
		}
		return nil
	case s.Publish != nil:
		req := wire.Request{Saga: in.Request.Saga, Step: expand(s.Publish.Step, env, in), Payload: in.Request.Payload, Trace: in.Request.Trace}
		if _, err := env.Send(ctx, s.Publish.Edge, in.Meta, req, 0); err != nil {
			return fmt.Errorf("%s: %w", s.Publish.Edge, err)
		}
		return nil
	case s.Parallel != nil:
		errc := make(chan error, len(s.Parallel))
		for _, sub := range s.Parallel {
			sub := sub
			go func() {
				sub2 := &Result{Status: 200, Body: map[string]any{}}
				errc <- runStep(ctx, env, sub, in, sub2)
			}()
		}
		var first error
		for range s.Parallel {
			if err := <-errc; err != nil && first == nil {
				first = err
			}
		}
		return first
	case s.Reply != nil:
		if s.Reply.Status != 0 {
			r.Status = s.Reply.Status
		}
		if s.Reply.Outcome != "" {
			r.Outcome = s.Reply.Outcome
		}
		return errStop
	case s.Outcome != nil:
		env.ReportOutcome(in.Request.Saga, s.Outcome.Value)
		r.Outcome = s.Outcome.Value
		return nil
	}
	return errors.New("empty step")
}

// expand substitutes {saga}, {step}, {node}, {payload}, {edge} and {cfg.key}.
func expand(s string, env Env, in Input) string {
	if !strings.Contains(s, "{") {
		return s
	}
	rep := strings.NewReplacer(
		"{saga}", in.Request.Saga,
		"{step}", in.Request.Step,
		"{node}", env.Node(),
		"{edge}", in.Link.Edge,
		"{payload}", string(in.Request.Payload),
	)
	s = rep.Replace(s)
	for {
		i := strings.Index(s, "{cfg.")
		if i < 0 {
			break
		}
		j := strings.Index(s[i:], "}")
		if j < 0 {
			break
		}
		key := s[i+5 : i+j]
		s = s[:i] + env.ConfigString(key) + s[i+j+1:]
	}
	return s
}
