// Package runtime abstracts the container backend. The engine only talks to
// the Runtime interface; FakeRuntime simulates containers in memory and
// DockerRuntime is the stub where the real Docker client will go.
package runtime

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotImplemented = errors.New("runtime: not implemented")
	ErrNotFound       = errors.New("runtime: container not found")
	ErrExists         = errors.New("runtime: container already exists")
	ErrInvalidState   = errors.New("runtime: invalid state transition")
)

// State is the container lifecycle state as reported by Inspect.
type State string

const (
	StatePending  State = "pending"  // created, not started
	StateStarting State = "starting" // started, health check not yet passing
	StateHealthy  State = "healthy"
	StatePaused   State = "paused"
	StateStopped  State = "stopped"
	StateFailed   State = "failed"
)

// ContainerSpec is everything needed to create one container.
type ContainerSpec struct {
	Name    string // unique; the engine uses "<nodeID>" or "<nodeID>-<n>"
	Image   string
	Network string
	Env     map[string]string
	Labels  map[string]string // symphony.node, symphony.kind, symphony.topology
}

// Container is the runtime's view of one container.
type Container struct {
	ID        string
	Name      string
	Image     string
	State     State
	CreatedAt time.Time
	StartedAt time.Time
	Labels    map[string]string
}

// Stats is a point-in-time resource sample.
type Stats struct {
	CPUPercent  float64
	MemoryBytes uint64
	NetRxBytes  uint64
	NetTxBytes  uint64
	SampledAt   time.Time
}

// Runtime is the container backend contract.
type Runtime interface {
	EnsureNetwork(ctx context.Context, name string) error
	Create(ctx context.Context, spec ContainerSpec) (Container, error)
	Destroy(ctx context.Context, name string) error
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
	Pause(ctx context.Context, name string) error
	Unpause(ctx context.Context, name string) error
	Inspect(ctx context.Context, name string) (Container, error)
	Stats(ctx context.Context, name string) (Stats, error)
	List(ctx context.Context) ([]Container, error)
}

// StateListener is implemented by runtimes that can push state changes
// (the fake does; Docker will via the events API). The engine registers
// itself so the UI animates without polling.
type StateListener interface {
	OnStateChange(fn func(Container))
}
