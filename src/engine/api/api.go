// Package api is the Wails-bound surface. Every exported method on API is
// callable from the frontend through the generated bindings in
// src/client/wailsjs/go/api/API.js; keep the signatures JSON-friendly.
package api

import (
	"context"
	"errors"

	wailsrt "github.com/wailsapp/wails/v2/pkg/runtime"

	"symphony/engine/core"
	"symphony/engine/events"
	"symphony/engine/planner"
	"symphony/engine/spec"
)

// API holds the engine and forwards bus events to the webview.
type API struct {
	eng   *core.Engine
	bus   *events.Bus
	ctx   context.Context
	unsub func()
}

func New(eng *core.Engine, bus *events.Bus) *API {
	return &API{eng: eng, bus: bus}
}

// Startup wires the event bus to Wails. It is a package function rather
// than a method so it is not exposed as a binding.
func Startup(a *API, ctx context.Context) {
	a.ctx = ctx
	a.unsub = a.bus.Subscribe(func(ev events.Event) {
		wailsrt.EventsEmit(ctx, string(ev.Type), ev.Payload)
	})
	a.bus.Log("info", "engine", "symphony ready (fake runtime)")
}

// Shutdown detaches from the bus.
func Shutdown(a *API) {
	if a.unsub != nil {
		a.unsub()
	}
}

func (a *API) context() context.Context {
	if a.ctx == nil {
		return context.Background()
	}
	return a.ctx
}

// GetTopology returns the topology that is currently applied (running).
func (a *API) GetTopology() spec.Topology {
	return a.eng.Current()
}

// GetPresets returns the seeded example topologies.
func (a *API) GetPresets() []spec.Topology {
	return spec.Presets()
}

// GetPhase returns the engine phase for the toolbar pill.
func (a *API) GetPhase() string {
	return string(a.eng.Phase())
}

// ValidateTopology returns all validation findings (errors and warnings).
func (a *API) ValidateTopology(t spec.Topology) []spec.ValidationError {
	out := t.Normalized().Validate()
	if out == nil {
		out = []spec.ValidationError{}
	}
	return out
}

// PlanApply returns the plan without executing. Validation errors are
// returned as an error so the promise rejects; warnings are logged.
func (a *API) PlanApply(t spec.Topology) (planner.Plan, error) {
	plan, findings := a.eng.Plan(t)
	if spec.HasErrors(findings) {
		msg := ""
		for _, f := range findings {
			if f.Severity == spec.SeverityError {
				if msg != "" {
					msg += "; "
				}
				msg += f.Error()
			}
		}
		return plan, errors.New("validation failed: " + msg)
	}
	for _, f := range findings {
		a.bus.Log("warn", "validate", f.Error())
	}
	return plan, nil
}

// Apply executes the plan, emitting apply:progress events; it returns when
// the plan finishes or fails.
func (a *API) Apply(t spec.Topology) error {
	return a.eng.Apply(a.context(), t)
}

// Teardown destroys everything that is running.
func (a *API) Teardown() error {
	return a.eng.Teardown(a.context())
}

// SetHotConfig pushes a hot-config patch to a running node immediately.
func (a *API) SetHotConfig(nodeID string, patch map[string]any) error {
	return a.eng.SetHotConfig(a.context(), nodeID, patch)
}

// InjectFault injects a fault into a running node.
func (a *API) InjectFault(nodeID string, fault spec.FaultSpec) error {
	return a.eng.InjectFault(a.context(), nodeID, fault)
}

// GetNodeStatus returns the current status of every running node.
func (a *API) GetNodeStatus() map[string]core.NodeStatus {
	return a.eng.NodeStatuses()
}
