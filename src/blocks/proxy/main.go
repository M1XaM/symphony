// Command proxy forwards traffic for every pair of edges that passes through
// it (A -> proxy -> B) and is where link faults ("toxics") are injected
// without touching either endpoint. Inbound faults apply on the A -> proxy
// edge, outbound faults on the proxy -> B edge; both are plain /control
// fault.set commands scoped to the edge.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"symphony/blocks/common/client"
	"symphony/blocks/common/control"
	"symphony/blocks/common/health"
	"symphony/blocks/common/links"
	"symphony/blocks/common/server"
	"symphony/blocks/common/wire"
)

func main() {
	b, err := server.New("proxy")
	if err != nil {
		log.Fatal(err)
	}
	// forwarding is transparent: no retries of its own, the caller decides
	b.Config.Define("proxy_retry_max", 0.0)

	pairs := map[string]string{}
	for _, p := range b.Links.Pairs {
		pairs[p.In] = p.Out
	}
	// an unpaired inbound link with exactly one outbound link is paired
	// implicitly, so a hand-started proxy still works
	if len(pairs) == 0 && len(b.Links.In) == 1 && len(b.Links.Out) == 1 {
		pairs[b.Links.In[0].Edge] = b.Links.Out[0].Edge
	}

	for _, l := range b.Links.In {
		l := l
		outEdge, ok := pairs[l.Edge]
		if !ok {
			b.HandleIn(l, func(in server.Inbound) (server.Reply, error) {
				return server.Reply{}, &server.Reject{Status: http.StatusBadGateway, Msg: "proxy: inbound edge " + l.Edge + " is not paired with an outbound edge", Cause: "links"}
			})
			continue
		}
		b.HandleIn(l, func(in server.Inbound) (server.Reply, error) {
			c, ok := b.Out(outEdge)
			if !ok {
				return server.Reply{}, &server.Reject{Status: http.StatusBadGateway, Msg: "proxy: no outbound link " + outEdge, Cause: "links"}
			}
			meta := in.Meta
			meta.Step = in.Request.Step
			res := c.Call(in.Ctx, meta, in.Request)
			if res.Err != nil {
				status := http.StatusBadGateway
				if res.Outcome == client.OutcomeTimeout {
					status = http.StatusGatewayTimeout
				}
				if res.Status >= 400 {
					status = res.Status
				}
				cause := "upstream"
				if res.Outcome == client.OutcomeDropped || res.Outcome == client.OutcomeRefused {
					cause = "fault:" + string(res.Outcome)
				}
				return server.Reply{}, &server.Reject{Status: status, Msg: "proxy: " + res.Err.Error(), Cause: cause, Outcome: res.Response.Outcome}
			}
			// pass the upstream's result and outcome through untouched
			out, _ := json.Marshal(passthrough{Result: res.Response.Result, Upstream: res.Response.Node})
			return server.Reply{Result: out, Outcome: res.Response.Outcome}, nil
		})
		in, out := l.Edge, outEdge
		b.Health.Register("pair:"+in, func() health.Check {
			c, ok := b.Out(out)
			if !ok {
				return health.Check{Status: health.Failing, Detail: map[string]any{"in": in, "out": out, "cause": "outbound link missing"}}
			}
			st := health.Healthy
			if c.TargetsUp() == 0 {
				st = health.Failing
			} else if c.TargetsUp() < len(c.Link.Targets) {
				st = health.Degraded
			}
			return health.Check{Status: st, Detail: map[string]any{"in": in, "out": out, "targets_up": c.TargetsUp(), "targets": len(c.Link.Targets)}}
		})
	}

	b.Control.Handle("pairs.get", func(cmd control.Command) (any, error) { return pairs, nil })
	b.Control.Handle("route.set", func(cmd control.Command) (any, error) {
		// re-point an outbound edge at different targets at runtime
		edge := cmd.String("edge", cmd.Target.Edge)
		raw, _ := cmd.Params["targets"].([]any)
		if edge == "" || len(raw) == 0 {
			return nil, fmt.Errorf("%w: params.edge and params.targets required", control.ErrBadRequest)
		}
		targets := make([]string, 0, len(raw))
		for _, t := range raw {
			if s, ok := t.(string); ok {
				targets = append(targets, s)
			}
		}
		l, ok := b.Links.Outbound(edge)
		if !ok {
			return nil, fmt.Errorf("%w: no outbound link %q", control.ErrBadRequest, edge)
		}
		l.Targets = targets
		if err := b.ReplaceOut(l); err != nil {
			return nil, err
		}
		return l, nil
	})

	b.SetReady(true)
	if err := b.Run(); err != nil {
		log.Fatal(err)
	}
}

// passthrough is what the proxy replies: the upstream's result wrapped so the
// caller sees the same outcome it would have seen directly.
type passthrough struct {
	Result   json.RawMessage `json:"result,omitempty"`
	Upstream string          `json:"upstream"`
}

var _ links.Link
var _ wire.Meta
