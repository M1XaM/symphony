// Package api is the Wails-bound surface. Every exported method on API is
// callable from the frontend through the generated bindings in
// src/client/wailsjs/go/api/API.js; keep the signatures JSON-friendly.
package api

import (
	"context"
	"encoding/json"
	"errors"

	wailsrt "github.com/wailsapp/wails/v2/pkg/runtime"

	"symphony/blocks/common/control"
	"symphony/engine/blockclient"
	"symphony/engine/core"
	"symphony/engine/events"
	"symphony/engine/hoststats"
	"symphony/engine/library"
	"symphony/engine/logstream"
	"symphony/engine/metrics"
	"symphony/engine/planner"
	"symphony/engine/runtime"
	"symphony/engine/spec"
	"symphony/engine/tests"
)

// API holds the engine and forwards bus events to the webview.
type API struct {
	eng    *core.Engine
	bus    *events.Bus
	lib    *library.Library
	host   *hoststats.Sampler
	col    *metrics.Collector
	tests  *tests.Runner
	logs   *logstream.Streamer
	rtInfo RuntimeInfo
	ctx    context.Context
	unsub  func()
}

// RuntimeInfo says which backend the engine runs on.
type RuntimeInfo struct {
	Kind   string `json:"kind"` // docker | fake
	Detail string `json:"detail"`
}

// Deps are what the API needs.
type Deps struct {
	Engine    *core.Engine
	Bus       *events.Bus
	Library   *library.Library
	Host      *hoststats.Sampler
	Collector *metrics.Collector
	Tests     *tests.Runner
	Logs      *logstream.Streamer
	Runtime   RuntimeInfo
}

func New(d Deps) *API {
	return &API{eng: d.Engine, bus: d.Bus, lib: d.Library, host: d.Host, col: d.Collector, tests: d.Tests, logs: d.Logs, rtInfo: d.Runtime}
}

// Startup wires the event bus to Wails. It is a package function rather
// than a method so it is not exposed as a binding.
func Startup(a *API, ctx context.Context) {
	a.ctx = ctx
	a.unsub = a.bus.Subscribe(func(ev events.Event) {
		wailsrt.EventsEmit(ctx, string(ev.Type), ev.Payload)
	})
	a.bus.Log("info", "engine", "symphony ready ("+a.rtInfo.Kind+" runtime, "+a.rtInfo.Detail+")")
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

// GetRuntimeInfo reports the container backend in use.
func (a *API) GetRuntimeInfo() RuntimeInfo { return a.rtInfo }

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

// NewNode returns a node of a kind with the defaults for it (image, config,
// resources), so the palette and the engine agree on what a new node is.
func (a *API) NewNode(id string, kind string, pos spec.Position) spec.Node {
	return spec.NewNode(id, spec.NodeKind(kind), pos)
}

// GetUserTemplates returns the templates saved on this machine.
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

// GetLogHistory returns the log lines emitted before the webview attached.
func (a *API) GetLogHistory() []events.LogPayload {
	return a.bus.LogHistory()
}

// GetHostStats returns the host usage samples taken so far, oldest first.
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

// PlanApply returns the plan without executing.
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

// SetResources changes a running node's CPU/RAM limits in place.
func (a *API) SetResources(deployment, nodeID string, r spec.Resources) error {
	return a.eng.SetResources(a.context(), deployment, nodeID, r)
}

// InjectFault injects a fault into a running node.
func (a *API) InjectFault(deployment, nodeID string, fault spec.FaultSpec) error {
	return a.eng.InjectFault(a.context(), deployment, nodeID, fault)
}

// ClearFaults removes every fault on a node.
func (a *API) ClearFaults(deployment, nodeID string) error {
	return a.eng.ClearFaults(a.context(), deployment, nodeID)
}

// ControlNode sends a raw /control command to a running node's block(s).
func (a *API) ControlNode(deployment, nodeID string, cmd control.Command) (control.Result, error) {
	return a.eng.Control(a.context(), deployment, nodeID, cmd)
}

// GetNodeLogs returns the last n log lines of a node.
func (a *API) GetNodeLogs(deployment, nodeID string, n int) ([]blockclient.LogLine, error) {
	out, err := a.eng.Logs(a.context(), deployment, nodeID, n)
	if out == nil {
		out = []blockclient.LogLine{}
	}
	return out, err
}

// ListContainers returns what `docker ps` prints: every container on the
// host, newest first with the running ones on top; all=true keeps the
// stopped ones too (`docker ps -a`).
func (a *API) ListContainers(all bool) ([]runtime.HostContainer, error) {
	lister, ok := a.eng.Runtime().(runtime.HostLister)
	if !ok {
		return []runtime.HostContainer{}, nil
	}
	out, err := lister.ListHost(a.context(), all)
	if out == nil {
		out = []runtime.HostContainer{}
	}
	return out, err
}

// ContainerLogStream reports whether container output is being mirrored into
// the app log, and whether the runtime can do it at all.
func (a *API) ContainerLogStream() LogStreamState {
	return LogStreamState{Available: a.logs.Available(), Enabled: a.logs.Enabled()}
}

// SetContainerLogStream turns the mirror on or off.
func (a *API) SetContainerLogStream(on bool) LogStreamState {
	a.logs.SetEnabled(on)
	return a.ContainerLogStream()
}

// LogStreamState is the Log tab's view of the container log mirror.
type LogStreamState struct {
	Available bool `json:"available"`
	Enabled   bool `json:"enabled"`
}

// GetMetrics returns the last metrics sample of a deployment ("" = all).
func (a *API) GetMetrics(deployment string) events.MetricsPayload {
	p, _ := a.col.Last(deployment)
	return p
}

// FaultKinds lists the fault kinds the engine understands.
func (a *API) FaultKinds() []string {
	out := make([]string, 0, len(spec.AllFaultKinds))
	for _, k := range spec.AllFaultKinds {
		out = append(out, string(k))
	}
	return out
}

// --- tests --------------------------------------------------------------------

// ListScenarios returns the predefined scenarios.
func (a *API) ListScenarios() []tests.Scenario {
	return a.tests.Scenarios()
}

// StartTest runs a scenario against a template; params override the
// scenario's defaults.
func (a *API) StartTest(scenario string, template spec.Topology, params map[string]any) (string, error) {
	return a.tests.Start(a.context(), scenario, template, params)
}

// AbortTest stops a running scenario.
func (a *API) AbortTest(runID string) error {
	return a.tests.Abort(runID)
}

// ListRuns returns the stored results, newest first.
func (a *API) ListRuns() ([]tests.RunSummary, error) {
	out, err := a.tests.ListRuns()
	if out == nil {
		out = []tests.RunSummary{}
	}
	return out, err
}

// GetRun returns one stored run with its series.
func (a *API) GetRun(runID string) (tests.Run, error) {
	return a.tests.GetRun(runID)
}

// DeleteRun removes a stored run.
func (a *API) DeleteRun(runID string) error {
	return a.tests.DeleteRun(runID)
}

// ExportRun returns a run as pretty JSON.
func (a *API) ExportRun(runID string) (string, error) {
	r, err := a.tests.GetRun(runID)
	if err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	return string(b), err
}
