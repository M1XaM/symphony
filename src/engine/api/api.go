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
	"symphony/engine/hoststats"
	"symphony/engine/library"
	"symphony/engine/planner"
	"symphony/engine/spec"
)

// API holds the engine and forwards bus events to the webview.
type API struct {
	eng   *core.Engine
	bus   *events.Bus
	lib   *library.Library
	host  *hoststats.Sampler
	ctx   context.Context
	unsub func()
}

func New(eng *core.Engine, bus *events.Bus, lib *library.Library, host *hoststats.Sampler) *API {
	return &API{eng: eng, bus: bus, lib: lib, host: host}
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

// GetDeployments returns every topology that is currently applied, with its
// phase and per-node status, so the webview can pick up where the engine is.
func (a *API) GetDeployments() []core.DeploymentInfo {
	out := a.eng.Deployments()
	if out == nil {
		out = []core.DeploymentInfo{}
	}
	return out
}

// GetPresets returns the seeded example topologies.
func (a *API) GetPresets() []spec.Topology {
	return spec.Presets()
}

// GetUserTemplates returns the templates saved on this machine. They are
// listed alongside the built-in presets in the left panel.
func (a *API) GetUserTemplates() ([]spec.Topology, error) {
	return a.lib.List()
}

// SaveUserTemplate stores a template (replacing one of the same name) and
// returns the library as it now stands on disk.
func (a *API) SaveUserTemplate(t spec.Topology) ([]spec.Topology, error) {
	list, err := a.lib.Save(t)
	if err != nil {
		return nil, err
	}
	a.bus.Log("info", "library", "saved template \""+t.Name+"\" to "+a.lib.Path())
	return list, nil
}

// DeleteUserTemplate removes a saved template and returns what is left.
func (a *API) DeleteUserTemplate(name string) ([]spec.Topology, error) {
	return a.lib.Delete(name)
}

// GetLogHistory returns the log lines emitted before the webview attached, so
// the Log tab starts out complete instead of beginning wherever the frontend
// happened to subscribe.
func (a *API) GetLogHistory() []events.LogPayload {
	return a.bus.LogHistory()
}

// GetHostStats returns the host usage samples taken so far, oldest first, so
// the Stats tab has a graph as soon as it opens.
func (a *API) GetHostStats() []events.HostStatsPayload {
	return a.host.History()
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

// Apply executes the plan for the topology named in t, next to whatever
// else is running; it returns when the plan finishes or fails.
func (a *API) Apply(t spec.Topology) error {
	return a.eng.Apply(a.context(), t)
}

// Teardown destroys everything running under the named topology.
func (a *API) Teardown(name string) error {
	return a.eng.Teardown(a.context(), name)
}

// SetHotConfig pushes a hot-config patch to a running node immediately.
func (a *API) SetHotConfig(deployment, nodeID string, patch map[string]any) error {
	return a.eng.SetHotConfig(a.context(), deployment, nodeID, patch)
}

// InjectFault injects a fault into a running node.
func (a *API) InjectFault(deployment, nodeID string, fault spec.FaultSpec) error {
	return a.eng.InjectFault(a.context(), deployment, nodeID, fault)
}
