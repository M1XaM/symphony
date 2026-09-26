// Package runtime abstracts the container backend. The engine only talks to
// the Runtime interface; FakeRuntime simulates containers in memory and
// DockerRuntime drives the Docker Engine API.
package runtime

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	ErrNotFound     = errors.New("runtime: container not found")
	ErrExists       = errors.New("runtime: container already exists")
	ErrInvalidState = errors.New("runtime: invalid state transition")
)

// State is the container lifecycle state as reported by Inspect. The
// runtime only knows what the container engine knows; the block's own
// deep health (degraded / failing) is layered on top by the engine core.
type State string

const (
	StatePending  State = "pending"  // created, not started
	StateStarting State = "starting" // started, health check not yet passing
	StateHealthy  State = "healthy"
	StatePaused   State = "paused"
	StateStopped  State = "stopped"
	StateFailed   State = "failed"
)

// Resources are the cgroup limits for a container; zero = unlimited.
type Resources struct {
	CPUs     float64
	MemoryMB int
	PidsMax  int
}

// ContainerSpec is everything needed to create one container.
type ContainerSpec struct {
	Name      string // unique; "<slug>-<nodeID>" or "<slug>-<nodeID>-<n>"
	Image     string
	Network   string
	Alias     string // extra DNS name on the network (the node id)
	Env       map[string]string
	Labels    map[string]string // symphony.owner, symphony.deployment, symphony.node, symphony.kind
	Resources Resources
	// AdminPort is the container port to publish on the host loopback so the
	// engine can reach the block's admin API; 0 publishes nothing.
	AdminPort int
	DataPort  int
	// Healthcheck runs the block's own probe inside the container.
	Healthcheck bool
}

// Container is the runtime's view of one container.
type Container struct {
	ID        string
	Name      string
	Image     string
	State     State
	Health    string // none | starting | healthy | unhealthy (engine-level health check)
	ExitCode  int
	OOMKilled bool
	Error     string
	CreatedAt time.Time
	StartedAt time.Time
	Labels    map[string]string
	// AdminAddr is host:port of the published admin port, "" when not published.
	AdminAddr string
}

// HostContainer is one row of `docker ps`: what the daemon says about a
// container, whether or not Symphony created it. Fields mirror the columns
// the CLI prints, so the Containers tab can render them as they come.
type HostContainer struct {
	ID         string   `json:"id"` // short id, as the CLI prints it
	Name       string   `json:"name"`
	Image      string   `json:"image"`
	Command    string   `json:"command"`
	CreatedAt  int64    `json:"created_at"` // unix ms
	State      string   `json:"state"`      // running | exited | paused | created | restarting | dead
	Status     string   `json:"status"`     // "Up 3 minutes (healthy)"
	Health     string   `json:"health,omitempty"`
	Ports      []string `json:"ports"`
	Networks   []string `json:"networks"`
	Ours       bool     `json:"ours"` // carries this instance's owner label
	Deployment string   `json:"deployment,omitempty"`
	Node       string   `json:"node,omitempty"`
}

// HostLister is implemented by runtimes that can enumerate every container
// on the machine rather than only their own — what `docker ps` shows.
type HostLister interface {
	ListHost(ctx context.Context, all bool) ([]HostContainer, error)
}

// LogFollower is implemented by runtimes that can stream a container's
// output as it is written (Docker does; the fake has no real output). fn is
// called once per line and must not block for long; the call returns when
// the stream ends or ctx is cancelled.
type LogFollower interface {
	FollowLogs(ctx context.Context, name string, since time.Time, fn func(line string, stderr bool)) error
}

// Stats is a point-in-time resource sample.
type Stats struct {
	CPUPercent       float64
	MemoryBytes      uint64
	MemoryLimit      uint64
	NetRxBytes       uint64
	NetTxBytes       uint64
	ThrottledPeriods uint64
	SampledAt        time.Time
}

// Runtime is the container backend contract.
type Runtime interface {
	// Kind names the backend: "fake" or "docker".
	Kind() string

	EnsureNetwork(ctx context.Context, name string) error
	RemoveNetwork(ctx context.Context, name string) error

	ImageExists(ctx context.Context, ref string) (bool, error)
	PullImage(ctx context.Context, ref string, progress func(string)) error
	// BuildImage builds ref from a tar build context with the given
	// Dockerfile path (inside the context) and build args.
	BuildImage(ctx context.Context, ref string, buildContext io.Reader, dockerfile string, args map[string]string, progress func(string)) error

	Create(ctx context.Context, spec ContainerSpec) (Container, error)
	Destroy(ctx context.Context, name string) error
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
	Kill(ctx context.Context, name string) error
	Pause(ctx context.Context, name string) error
	Unpause(ctx context.Context, name string) error
	Update(ctx context.Context, name string, r Resources) error
	Connect(ctx context.Context, name, network string) error
	Disconnect(ctx context.Context, name, network string) error
	Inspect(ctx context.Context, name string) (Container, error)
	Stats(ctx context.Context, name string) (Stats, error)
	Logs(ctx context.Context, name string, tail int) ([]string, error)
	List(ctx context.Context) ([]Container, error)
}

// StateListener is implemented by runtimes that can push state changes
// (the fake does; Docker does via the events API). The engine registers
// itself so the UI animates without polling.
type StateListener interface {
	OnStateChange(fn func(Container))
}
