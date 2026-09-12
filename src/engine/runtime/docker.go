package runtime

import "context"

// DockerRuntime will wrap github.com/docker/docker/client. Every method
// currently returns ErrNotImplemented so the engine can be wired against it
// without a daemon present.
//
// Implementation notes for later:
//   - EnsureNetwork -> NetworkCreate with a "symphony" label, idempotent.
//   - Create        -> ContainerCreate with HostConfig.NetworkMode = spec.Network
//     and a HEALTHCHECK-driven readiness gate for Inspect.
//   - Inspect       -> ContainerInspect; map State.Health.Status to State.
//   - Stats         -> ContainerStats(stream=false), compute CPU% from deltas.
//   - OnStateChange -> client.Events filtered by label "symphony.node".
type DockerRuntime struct {
	Host string // e.g. unix:///var/run/docker.sock; empty = client defaults
}

var _ Runtime = (*DockerRuntime)(nil)

func NewDocker(host string) *DockerRuntime { return &DockerRuntime{Host: host} }

func (d *DockerRuntime) EnsureNetwork(context.Context, string) error { return ErrNotImplemented }
func (d *DockerRuntime) Create(context.Context, ContainerSpec) (Container, error) {
	return Container{}, ErrNotImplemented
}
func (d *DockerRuntime) Destroy(context.Context, string) error { return ErrNotImplemented }
func (d *DockerRuntime) Start(context.Context, string) error   { return ErrNotImplemented }
func (d *DockerRuntime) Stop(context.Context, string) error    { return ErrNotImplemented }
func (d *DockerRuntime) Pause(context.Context, string) error   { return ErrNotImplemented }
func (d *DockerRuntime) Unpause(context.Context, string) error { return ErrNotImplemented }
func (d *DockerRuntime) Inspect(context.Context, string) (Container, error) {
	return Container{}, ErrNotImplemented
}
func (d *DockerRuntime) Stats(context.Context, string) (Stats, error) {
	return Stats{}, ErrNotImplemented
}
func (d *DockerRuntime) List(context.Context) ([]Container, error) { return nil, ErrNotImplemented }
