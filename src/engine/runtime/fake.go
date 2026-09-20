package runtime

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"sort"
	"sync"
	"time"
)

// FakeOptions tune the simulated delays. Zero values pick defaults that make
// a 5-7 node topology apply in a few seconds.
type FakeOptions struct {
	CreateDelay   time.Duration // Create call latency
	StartMin      time.Duration // starting -> healthy, lower bound
	StartMax      time.Duration // starting -> healthy, upper bound
	DestroyDelay  time.Duration
	FailureChance float64 // probability a start ends in StateFailed (0 = never)
}

func (o FakeOptions) withDefaults() FakeOptions {
	if o.CreateDelay == 0 {
		o.CreateDelay = 120 * time.Millisecond
	}
	if o.StartMin == 0 {
		o.StartMin = 400 * time.Millisecond
	}
	if o.StartMax == 0 {
		o.StartMax = 1100 * time.Millisecond
	}
	if o.DestroyDelay == 0 {
		o.DestroyDelay = 150 * time.Millisecond
	}
	return o
}

// FakeRuntime keeps containers in memory and walks them through
// pending -> starting -> healthy on timers. It satisfies Runtime and
// StateListener, and is what the app falls back to without a Docker daemon.
type FakeRuntime struct {
	opts FakeOptions

	mu         sync.Mutex
	containers map[string]*Container
	resources  map[string]Resources
	networks   map[string]bool
	images     map[string]bool
	timers     map[string]*time.Timer
	listeners  []func(Container)
	rng        *rand.Rand
	seq        int
}

var (
	_ Runtime       = (*FakeRuntime)(nil)
	_ StateListener = (*FakeRuntime)(nil)
	_ HostLister    = (*FakeRuntime)(nil)
)

func NewFake(opts FakeOptions) *FakeRuntime {
	return &FakeRuntime{
		opts:       opts.withDefaults(),
		containers: map[string]*Container{},
		resources:  map[string]Resources{},
		networks:   map[string]bool{},
		images:     map[string]bool{},
		timers:     map[string]*time.Timer{},
		rng:        rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (f *FakeRuntime) Kind() string { return "fake" }

func (f *FakeRuntime) OnStateChange(fn func(Container)) {
	f.mu.Lock()
	f.listeners = append(f.listeners, fn)
	f.mu.Unlock()
}

// notify must be called without f.mu held.
func (f *FakeRuntime) notify(c Container) {
	f.mu.Lock()
	ls := append([]func(Container){}, f.listeners...)
	f.mu.Unlock()
	for _, l := range ls {
		l(c)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (f *FakeRuntime) EnsureNetwork(ctx context.Context, name string) error {
	if err := sleepCtx(ctx, f.opts.CreateDelay/2); err != nil {
		return err
	}
	f.mu.Lock()
	f.networks[name] = true
	f.mu.Unlock()
	return nil
}

func (f *FakeRuntime) RemoveNetwork(ctx context.Context, name string) error {
	f.mu.Lock()
	delete(f.networks, name)
	f.mu.Unlock()
	return nil
}

func (f *FakeRuntime) ImageExists(ctx context.Context, ref string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.images[ref], nil
}

func (f *FakeRuntime) PullImage(ctx context.Context, ref string, progress func(string)) error {
	if err := sleepCtx(ctx, f.opts.CreateDelay*2); err != nil {
		return err
	}
	if progress != nil {
		progress("pulled " + ref + " (fake)")
	}
	f.mu.Lock()
	f.images[ref] = true
	f.mu.Unlock()
	return nil
}

func (f *FakeRuntime) BuildImage(ctx context.Context, ref string, buildContext io.Reader, dockerfile string, args map[string]string, progress func(string)) error {
	if buildContext != nil {
		_, _ = io.Copy(io.Discard, buildContext)
	}
	if err := sleepCtx(ctx, f.opts.CreateDelay*3); err != nil {
		return err
	}
	if progress != nil {
		progress("built " + ref + " (fake)")
	}
	f.mu.Lock()
	f.images[ref] = true
	f.mu.Unlock()
	return nil
}

func (f *FakeRuntime) Create(ctx context.Context, spec ContainerSpec) (Container, error) {
	if err := sleepCtx(ctx, f.opts.CreateDelay); err != nil {
		return Container{}, err
	}
	f.mu.Lock()
	if _, ok := f.containers[spec.Name]; ok {
		f.mu.Unlock()
		return Container{}, fmt.Errorf("%w: %s", ErrExists, spec.Name)
	}
	f.seq++
	c := &Container{
		ID:        fmt.Sprintf("fake-%04d", f.seq),
		Name:      spec.Name,
		Image:     spec.Image,
		State:     StatePending,
		Health:    "none",
		CreatedAt: time.Now(),
		Labels:    spec.Labels,
	}
	f.containers[spec.Name] = c
	f.resources[spec.Name] = spec.Resources
	snap := *c
	f.mu.Unlock()
	f.notify(snap)
	return snap, nil
}

func (f *FakeRuntime) Start(ctx context.Context, name string) error {
	f.mu.Lock()
	c, ok := f.containers[name]
	if !ok {
		f.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if c.State == StateHealthy || c.State == StateStarting {
		f.mu.Unlock()
		return nil
	}
	c.State = StateStarting
	c.Health = "starting"
	c.StartedAt = time.Now()
	snap := *c
	delay := f.opts.StartMin + time.Duration(f.rng.Int63n(int64(f.opts.StartMax-f.opts.StartMin)+1))
	fail := f.opts.FailureChance > 0 && f.rng.Float64() < f.opts.FailureChance
	if t := f.timers[name]; t != nil {
		t.Stop()
	}
	f.timers[name] = time.AfterFunc(delay, func() {
		f.mu.Lock()
		c, ok := f.containers[name]
		if !ok || c.State != StateStarting {
			f.mu.Unlock()
			return
		}
		if fail {
			c.State = StateFailed
			c.Health = "unhealthy"
			c.ExitCode = 1
		} else {
			c.State = StateHealthy
			c.Health = "healthy"
		}
		snap := *c
		f.mu.Unlock()
		f.notify(snap)
	})
	f.mu.Unlock()
	f.notify(snap)
	return nil
}

func (f *FakeRuntime) transition(name string, from []State, to State) error {
	f.mu.Lock()
	c, ok := f.containers[name]
	if !ok {
		f.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	allowed := len(from) == 0
	for _, s := range from {
		if c.State == s {
			allowed = true
		}
	}
	if !allowed {
		f.mu.Unlock()
		return fmt.Errorf("%w: %s is %s, cannot go to %s", ErrInvalidState, name, c.State, to)
	}
	if t := f.timers[name]; t != nil {
		t.Stop()
		delete(f.timers, name)
	}
	c.State = to
	switch to {
	case StateHealthy:
		c.Health = "healthy"
	case StateStopped, StateFailed:
		c.Health = "none"
	}
	snap := *c
	f.mu.Unlock()
	f.notify(snap)
	return nil
}

func (f *FakeRuntime) Stop(ctx context.Context, name string) error {
	if err := sleepCtx(ctx, f.opts.DestroyDelay/2); err != nil {
		return err
	}
	return f.transition(name, nil, StateStopped)
}

func (f *FakeRuntime) Kill(ctx context.Context, name string) error {
	return f.transition(name, nil, StateStopped)
}

func (f *FakeRuntime) Pause(ctx context.Context, name string) error {
	return f.transition(name, []State{StateHealthy, StateStarting}, StatePaused)
}

func (f *FakeRuntime) Unpause(ctx context.Context, name string) error {
	return f.transition(name, []State{StatePaused}, StateHealthy)
}

func (f *FakeRuntime) Update(ctx context.Context, name string, r Resources) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.containers[name]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	f.resources[name] = r
	return nil
}

func (f *FakeRuntime) Connect(ctx context.Context, name, network string) error {
	return f.transition(name, []State{StateFailed}, StateHealthy)
}

func (f *FakeRuntime) Disconnect(ctx context.Context, name, network string) error {
	// a disconnected fake container looks failed to everyone else
	return f.transition(name, []State{StateHealthy, StateStarting}, StateFailed)
}

func (f *FakeRuntime) Destroy(ctx context.Context, name string) error {
	if err := sleepCtx(ctx, f.opts.DestroyDelay); err != nil {
		return err
	}
	f.mu.Lock()
	c, ok := f.containers[name]
	if !ok {
		f.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if t := f.timers[name]; t != nil {
		t.Stop()
		delete(f.timers, name)
	}
	delete(f.containers, name)
	delete(f.resources, name)
	snap := *c
	snap.State = StateStopped
	f.mu.Unlock()
	f.notify(snap)
	return nil
}

func (f *FakeRuntime) Inspect(ctx context.Context, name string) (Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[name]
	if !ok {
		return Container{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return *c, nil
}

func (f *FakeRuntime) Stats(ctx context.Context, name string) (Stats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[name]
	if !ok {
		return Stats{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	base := 0.0
	if c.State == StateHealthy {
		base = 8 + f.rng.Float64()*20
	}
	limit := uint64(f.resources[name].MemoryMB) << 20
	return Stats{
		CPUPercent:  base,
		MemoryBytes: 64<<20 + uint64(f.rng.Int63n(32<<20)),
		MemoryLimit: limit,
		NetRxBytes:  uint64(f.rng.Int63n(1 << 20)),
		NetTxBytes:  uint64(f.rng.Int63n(1 << 20)),
		SampledAt:   time.Now(),
	}, nil
}

func (f *FakeRuntime) Logs(ctx context.Context, name string, tail int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return []string{fmt.Sprintf("%s fake container %s is %s", time.Now().Format(time.RFC3339), name, c.State)}, nil
}

func (f *FakeRuntime) List(ctx context.Context) ([]Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Container, 0, len(f.containers))
	for _, c := range f.containers {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ListHost presents the simulated containers in the shape `docker ps` uses.
// There is no daemon behind the fake, so this is everything it knows.
func (f *FakeRuntime) ListHost(ctx context.Context, all bool) ([]HostContainer, error) {
	cs, err := f.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]HostContainer, 0, len(cs))
	for _, c := range cs {
		state, status := fakeStatus(c)
		if !all && state != "running" && state != "paused" {
			continue
		}
		out = append(out, HostContainer{
			ID:         c.ID,
			Name:       c.Name,
			Image:      c.Image,
			Command:    "/block",
			CreatedAt:  c.CreatedAt.UnixMilli(),
			State:      state,
			Status:     status,
			Health:     c.Health,
			Ports:      []string{},
			Networks:   []string{},
			Ours:       true,
			Deployment: c.Labels[LabelDeployment],
			Node:       c.Labels[LabelNode],
		})
	}
	return out, nil
}

// fakeStatus maps a simulated state onto the daemon's state/status pair.
func fakeStatus(c Container) (state, status string) {
	switch c.State {
	case StatePending:
		return "created", "Created"
	case StatePaused:
		return "paused", "Up " + humanDuration(time.Since(c.StartedAt)) + " (Paused)"
	case StateStopped:
		return "exited", fmt.Sprintf("Exited (%d) %s ago", c.ExitCode, humanDuration(time.Since(c.CreatedAt)))
	case StateFailed:
		return "exited", fmt.Sprintf("Exited (%d) %s ago", c.ExitCode, humanDuration(time.Since(c.CreatedAt)))
	case StateStarting:
		return "running", "Up " + humanDuration(time.Since(c.StartedAt)) + " (health: starting)"
	default:
		return "running", "Up " + humanDuration(time.Since(c.StartedAt))
	}
}

// humanDuration is the CLI's coarse "3 minutes" rendering.
func humanDuration(d time.Duration) string {
	switch {
	case d < 2*time.Second:
		return "Less than a second"
	case d < time.Minute:
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	case d < 2*time.Minute:
		return "About a minute"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days", int(d.Hours())/24)
	}
}

func (f *FakeRuntime) GC(ctx context.Context) (int, error) {
	f.mu.Lock()
	n := len(f.containers) + len(f.networks)
	for name, t := range f.timers {
		t.Stop()
		delete(f.timers, name)
	}
	f.containers = map[string]*Container{}
	f.networks = map[string]bool{}
	f.mu.Unlock()
	return n, nil
}
