// Package api is the surface both front ends drive the system through: the
// desktop app binds it to the webview with Wails (every exported method on
// API is callable from the frontend through the generated bindings in
// src/client/wailsjs/go/api/API.js), and the CLI calls the same methods.
// Keep the signatures JSON-friendly.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"symphony/blocks/common/control"
	"symphony/engine/blockclient"
	"symphony/engine/core"
	"symphony/engine/events"
	"symphony/engine/hoststats"
	"symphony/engine/library"
	"symphony/engine/logstream"
	"symphony/engine/planner"
	"symphony/engine/report"
	"symphony/engine/runtime"
	"symphony/engine/spec"
	"symphony/engine/tests"
)

// API holds the engine and forwards bus events to the front end.
type API struct {
	eng   *core.Engine
	bus   *events.Bus
	lib   *library.Library
	host  *hoststats.Sampler
	tests *tests.Runner
	ctx   context.Context
	unsub func()

	mu     sync.Mutex
	docker events.RuntimeStatusPayload
	// saveDialog asks the user where to save a file; nil outside the window
	saveDialog SaveDialog
	// openDialog asks which file to open; nil outside the window
	openDialog OpenDialog
}

// SaveDialog asks where to save a file of the given kind ("md", "pdf",
// "json"), offering name. It returns "" when the user cancels.
type SaveDialog func(name, format string) (string, error)

// SetSaveDialog gives the API the window's save dialog. A package function,
// so it is not exposed as a binding.
func SetSaveDialog(a *API, fn SaveDialog) { a.saveDialog = fn }

// ErrNoDocker is returned by every call that needs containers while the
// Docker daemon does not answer.
var ErrNoDocker = errors.New("Docker is not available: install Docker and start it, then try again")

// Deps are what the API needs.
type Deps struct {
	Engine  *core.Engine
	Bus     *events.Bus
	Library *library.Library
	Host    *hoststats.Sampler
	Tests   *tests.Runner
}

func New(d Deps) *API {
	return &API{eng: d.Engine, bus: d.Bus, lib: d.Library, host: d.Host, tests: d.Tests,
		docker: events.RuntimeStatusPayload{Detail: "checking for the Docker daemon"}}
}

// Startup starts a session: calls run under ctx, and emit (when not nil)
// receives every bus event by name; the desktop app passes Wails'
// EventsEmit. It is a package function rather than a method so it is not
// exposed as a binding.
func Startup(a *API, ctx context.Context, emit func(name string, payload any)) {
	a.ctx = ctx
	if emit != nil {
		a.unsub = a.bus.Subscribe(func(ev events.Event) { emit(string(ev.Type), ev.Payload) })
	}
	a.bus.Log("info", "engine", "symphony ready")
}

// SetRuntimeStatus records whether the Docker daemon answers and tells the
// webview. A package function, so it is not exposed as a binding.
func SetRuntimeStatus(a *API, st events.RuntimeStatusPayload) {
	a.mu.Lock()
	a.docker = st
	a.mu.Unlock()
	a.bus.Publish(events.RuntimeStatus, st)
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

// GetRuntimeStatus reports whether the Docker daemon answers.
func (a *API) GetRuntimeStatus() events.RuntimeStatusPayload {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.docker
}

func (a *API) dockerUp() bool { return a.GetRuntimeStatus().Available }

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

// ExportTemplate writes a template to a JSON file the user picks, in the
// form `symphony-cli apply <file>` reads.
func (a *API) ExportTemplate(t spec.Topology) (ExportResult, error) {
	if a.saveDialog == nil {
		return ExportResult{}, errors.New("no save dialog: templates are exported from the window, or with symphony-cli show <template>")
	}
	t = t.Normalized()
	doc, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return ExportResult{}, err
	}
	file := strings.Trim(spec.Slug(t.Name), "-")
	if file == "" {
		file = "template"
	}
	path, err := a.saveDialog(file+".json", "json")
	if err != nil {
		return ExportResult{}, err
	}
	if path == "" {
		return ExportResult{Cancelled: true}, nil
	}
	if err := os.WriteFile(path, append(doc, '\n'), 0o644); err != nil {
		return ExportResult{}, err
	}
	a.bus.Log("info", "library", fmt.Sprintf("template %q exported to %s", t.Name, path))
	return ExportResult{Path: path}, nil
}

// OpenDialog asks which file of the given kind ("json") to open. It returns
// "" when the user cancels.
type OpenDialog func(format string) (string, error)

// SetOpenDialog gives the API the window's open dialog. A package function,
// so it is not exposed as a binding.
func SetOpenDialog(a *API, fn OpenDialog) { a.openDialog = fn }

// ImportResult is the template read from the file the user picked, or that
// the user cancelled.
type ImportResult struct {
	Topology  spec.Topology `json:"topology"`
	Cancelled bool          `json:"cancelled"`
}

// ImportTemplate reads a template from a JSON file the user picks: what
// ExportTemplate and `symphony-cli show` write. Nothing is stored; the
// template goes onto the map, to be saved from there.
func (a *API) ImportTemplate() (ImportResult, error) {
	if a.openDialog == nil {
		return ImportResult{}, errors.New("no open dialog: templates are imported from the window, or with symphony-cli save <file>")
	}
	path, err := a.openDialog("json")
	if err != nil {
		return ImportResult{}, err
	}
	if path == "" {
		return ImportResult{Cancelled: true}, nil
	}
	t, err := ReadTopology(path)
	if err != nil {
		return ImportResult{}, err
	}
	a.bus.Log("info", "library", fmt.Sprintf("template %q imported from %s", t.Name, path))
	return ImportResult{Topology: t}, nil
}

// ReadTopology reads a topology from a JSON file.
func ReadTopology(path string) (spec.Topology, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return spec.Topology{}, err
	}
	var t spec.Topology
	if err := json.Unmarshal(b, &t); err != nil {
		return spec.Topology{}, fmt.Errorf("%s is not a template: %w", filepath.Base(path), err)
	}
	if len(t.Nodes) == 0 {
		return spec.Topology{}, fmt.Errorf("%s is not a template: it has no nodes", filepath.Base(path))
	}
	return t.Normalized(), nil
}

// GenerateTopology draws a random saga topology under the given name: bias 0
// leans to orchestration, 1 to choreography. See spec.Generate.
func (a *API) GenerateTopology(bias float64, name string) spec.Topology {
	return spec.Generate(bias, name, rand.New(rand.NewSource(time.Now().UnixNano())))
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
	if !a.dockerUp() {
		return ErrNoDocker
	}
	return a.eng.Apply(a.context(), t)
}

// Teardown destroys everything running under the named topology.
func (a *API) Teardown(name string) error {
	if !a.dockerUp() {
		return ErrNoDocker
	}
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
	out, err := a.eng.Runtime().ListHost(a.context(), all)
	if out == nil {
		out = []runtime.HostContainer{}
	}
	return out, err
}

// containerLogTail is how many lines of a container's log the Log tab reads
// when it is asked for all of them.
const containerLogTail = 2000

// GetContainerLogs returns the last n lines (0 = as many as the Log tab
// keeps) one of Symphony's containers wrote: what `docker logs` prints for
// it, each line parsed the way the blocks write them.
func (a *API) GetContainerLogs(name string, n int) ([]events.LogPayload, error) {
	if !a.dockerUp() {
		return nil, ErrNoDocker
	}
	if n <= 0 || n > containerLogTail {
		n = containerLogTail
	}
	rt := a.eng.Runtime()
	cs, err := rt.ListHost(a.context(), true)
	if err != nil {
		return nil, err
	}
	ours := false
	for _, c := range cs {
		if c.Ours && c.Name == name {
			ours = true
			break
		}
	}
	if !ours {
		return nil, fmt.Errorf("%q is not one of Symphony's containers", name)
	}
	raw, err := rt.Logs(a.context(), name, n)
	if err != nil {
		return nil, err
	}
	out := make([]events.LogPayload, 0, len(raw))
	for _, line := range raw {
		if line != "" {
			out = append(out, logstream.Entry(name, line))
		}
	}
	return out, nil
}

// --- tests --------------------------------------------------------------------

// ListScenarios returns the predefined scenarios.
func (a *API) ListScenarios() []tests.Scenario {
	return a.tests.Scenarios()
}

// StartTest runs a scenario against a template; params override the
// scenario's defaults.
func (a *API) StartTest(scenario string, template spec.Topology, params map[string]any) (string, error) {
	if !a.dockerUp() {
		return "", ErrNoDocker
	}
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

// ExportResult says where a report was saved, or that the user cancelled.
type ExportResult struct {
	Path      string `json:"path"`
	Cancelled bool   `json:"cancelled"`
}

// ExportRunReport writes a stored run's report ("md" or "pdf"): the report
// is rendered first, then the user is asked where to save it.
func (a *API) ExportRunReport(runID, format string) (ExportResult, error) {
	if a.saveDialog == nil {
		return ExportResult{}, errors.New("no save dialog: reports are exported from the window, or with symphony-cli run <id> --report-md FILE")
	}
	run, err := a.tests.GetRun(runID)
	if err != nil {
		return ExportResult{}, err
	}
	doc, err := report.Render(run, format)
	if err != nil {
		return ExportResult{}, err
	}
	path, err := a.saveDialog(report.FileName(run.ID, format), format)
	if err != nil {
		return ExportResult{}, err
	}
	if path == "" {
		return ExportResult{Cancelled: true}, nil
	}
	if err := os.WriteFile(path, doc, 0o644); err != nil {
		return ExportResult{}, err
	}
	a.bus.Log("info", "test", fmt.Sprintf("report of %s saved to %s", run.ID, path))
	return ExportResult{Path: path}, nil
}

// DeleteRun removes a stored run.
func (a *API) DeleteRun(runID string) error {
	return a.tests.DeleteRun(runID)
}
