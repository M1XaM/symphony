package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// LabelOwner marks everything one Symphony instance creates, so it can be
// found, adopted and garbage-collected even after a crash.
const (
	LabelOwner      = "symphony.owner"
	LabelDeployment = "symphony.deployment"
	LabelNode       = "symphony.node"
	LabelKind       = "symphony.kind"
	LabelTopology   = "symphony.topology"
)

// adminContainerPort is the port every block serves its admin API on inside
// the container; the engine publishes it on the host loopback.
const adminContainerPort = 9090

// DockerRuntime drives the Docker Engine API. Every object it creates
// carries LabelOwner=owner.
type DockerRuntime struct {
	cli   *client.Client
	owner string

	mu        sync.Mutex
	listeners []func(Container)
	events    context.CancelFunc
	prevStats map[string]container.StatsResponse
}

var (
	_ Runtime       = (*DockerRuntime)(nil)
	_ StateListener = (*DockerRuntime)(nil)
	_ HostLister    = (*DockerRuntime)(nil)
	_ LogFollower   = (*DockerRuntime)(nil)
)

// NewDocker returns a runtime for the daemon DOCKER_HOST (or the platform
// default) points at. It does not connect: the daemon may not be up yet, and
// may come and go while the app runs. Ping and Watch say when it answers.
func NewDocker(owner string) (*DockerRuntime, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &DockerRuntime{cli: cli, owner: owner, prevStats: map[string]container.StatsResponse{}}, nil
}

// Ping asks the daemon whether it is up and returns its API version.
func (d *DockerRuntime) Ping(ctx context.Context) (string, error) {
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ping, err := d.cli.Ping(pctx, client.PingOptions{NegotiateAPIVersion: true})
	if err != nil {
		return "", fmt.Errorf("docker daemon not reachable: %w", err)
	}
	return "api " + ping.APIVersion, nil
}

// Watch pings the daemon until ctx ends and calls fn whenever it comes or
// goes; up is what the caller saw last. It asks every couple of seconds
// while the daemon is down, so a Docker started after the app is noticed at
// once, and rarely while it is up.
func (d *DockerRuntime) Watch(ctx context.Context, up bool, fn func(up bool, detail string)) {
	for {
		wait := 10 * time.Second
		if !up {
			wait = 2 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		detail, err := d.Ping(ctx)
		if ctx.Err() != nil {
			return
		}
		if (err == nil) != up {
			up = err == nil
			if err != nil {
				detail = err.Error()
			}
			fn(up, detail)
		}
	}
}

// Close stops the event stream and the client.
func (d *DockerRuntime) Close() error {
	d.mu.Lock()
	if d.events != nil {
		d.events()
		d.events = nil
	}
	d.mu.Unlock()
	return d.cli.Close()
}

func (d *DockerRuntime) ownerFilter() client.Filters {
	return client.Filters{}.Add("label", LabelOwner+"="+d.owner)
}

// --- networks --------------------------------------------------------------

func (d *DockerRuntime) EnsureNetwork(ctx context.Context, name string) error {
	if _, err := d.cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{}); err == nil {
		return nil
	} else if !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("network inspect %s: %w", name, err)
	}
	_, err := d.cli.NetworkCreate(ctx, name, client.NetworkCreateOptions{
		Driver: "bridge",
		Labels: map[string]string{LabelOwner: d.owner, LabelDeployment: strings.TrimPrefix(name, "symphony-")},
	})
	if err != nil && !cerrdefs.IsConflict(err) {
		return fmt.Errorf("network create %s: %w", name, err)
	}
	return nil
}

func (d *DockerRuntime) RemoveNetwork(ctx context.Context, name string) error {
	_, err := d.cli.NetworkRemove(ctx, name, client.NetworkRemoveOptions{})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("network remove %s: %w", name, err)
	}
	return nil
}

func (d *DockerRuntime) Connect(ctx context.Context, name, network string) error {
	_, err := d.cli.NetworkConnect(ctx, network, client.NetworkConnectOptions{Container: name})
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("network connect %s: %w", name, err)
	}
	return nil
}

func (d *DockerRuntime) Disconnect(ctx context.Context, name, network string) error {
	_, err := d.cli.NetworkDisconnect(ctx, network, client.NetworkDisconnectOptions{Container: name, Force: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("network disconnect %s: %w", name, err)
	}
	return nil
}

// --- images ----------------------------------------------------------------

func (d *DockerRuntime) ImageExists(ctx context.Context, ref string) (bool, error) {
	_, err := d.cli.ImageInspect(ctx, ref)
	if err == nil {
		return true, nil
	}
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	return false, fmt.Errorf("image inspect %s: %w", ref, err)
}

func (d *DockerRuntime) PullImage(ctx context.Context, ref string, progress func(string)) error {
	resp, err := d.cli.ImagePull(ctx, ref, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("image pull %s: %w", ref, err)
	}
	defer resp.Close()
	return readJSONStream(resp, progress)
}

func (d *DockerRuntime) BuildImage(ctx context.Context, ref string, buildContext io.Reader, dockerfile string, args map[string]string, progress func(string)) error {
	buildArgs := map[string]*string{}
	for k, v := range args {
		v := v
		buildArgs[k] = &v
	}
	res, err := d.cli.ImageBuild(ctx, buildContext, client.ImageBuildOptions{
		Tags:       []string{ref},
		Dockerfile: dockerfile,
		BuildArgs:  buildArgs,
		Remove:     true,
		Labels:     map[string]string{LabelOwner: d.owner},
	})
	if err != nil {
		return fmt.Errorf("image build %s: %w", ref, err)
	}
	defer res.Body.Close()
	return readJSONStream(res.Body, progress)
}

// readJSONStream consumes the daemon's progress stream, forwarding lines
// and surfacing an error message as an error.
func readJSONStream(r io.Reader, progress func(string)) error {
	dec := json.NewDecoder(r)
	last := ""
	for {
		var m struct {
			Stream string `json:"stream"`
			Status string `json:"status"`
			ID     string `json:"id"`
			Error  string `json:"error"`
		}
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if m.Error != "" {
			return errors.New(m.Error)
		}
		line := strings.TrimSpace(m.Stream)
		if line == "" && m.Status != "" && !strings.Contains(m.Status, "Downloading") && !strings.Contains(m.Status, "Extracting") {
			line = m.Status
			if m.ID != "" {
				line = m.ID + ": " + line
			}
		}
		if line != "" && line != last && progress != nil {
			progress(line)
			last = line
		}
	}
}

// --- containers ------------------------------------------------------------

func (d *DockerRuntime) Create(ctx context.Context, spec ContainerSpec) (Container, error) {
	env := make([]string, 0, len(spec.Env))
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	labels := map[string]string{LabelOwner: d.owner}
	for k, v := range spec.Labels {
		labels[k] = v
	}
	cfg := &container.Config{
		Image:        spec.Image,
		Env:          env,
		Labels:       labels,
		ExposedPorts: network.PortSet{},
	}
	host := &container.HostConfig{
		NetworkMode:    container.NetworkMode(spec.Network),
		PortBindings:   network.PortMap{},
		RestartPolicy:  container.RestartPolicy{Name: container.RestartPolicyDisabled},
		ReadonlyRootfs: true,
		CapDrop:        []string{"ALL"},
		SecurityOpt:    []string{"no-new-privileges"},
		LogConfig:      container.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "5m", "max-file": "2"}},
		Resources:      toDockerResources(spec.Resources),
	}
	if spec.DataPort > 0 {
		if p, ok := network.PortFrom(uint16(spec.DataPort), network.TCP); ok {
			cfg.ExposedPorts[p] = struct{}{}
		}
	}
	if spec.AdminPort > 0 {
		if p, ok := network.PortFrom(uint16(spec.AdminPort), network.TCP); ok {
			cfg.ExposedPorts[p] = struct{}{}
			host.PortBindings[p] = []network.PortBinding{{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "0"}}
		}
	}
	if spec.Healthcheck {
		cfg.Healthcheck = &container.HealthConfig{
			Test:        []string{"CMD", "/block", "-healthcheck"},
			Interval:    2 * time.Second,
			Timeout:     2 * time.Second,
			StartPeriod: 5 * time.Second,
			Retries:     3,
		}
	}
	var netcfg *network.NetworkingConfig
	if spec.Network != "" {
		ep := &network.EndpointSettings{}
		if spec.Alias != "" {
			ep.Aliases = []string{spec.Alias}
		}
		netcfg = &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{spec.Network: ep}}
	}
	res, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{Name: spec.Name, Config: cfg, HostConfig: host, NetworkingConfig: netcfg})
	if err != nil {
		if cerrdefs.IsConflict(err) {
			return Container{}, fmt.Errorf("%w: %s", ErrExists, spec.Name)
		}
		return Container{}, fmt.Errorf("container create %s: %w", spec.Name, err)
	}
	return Container{ID: res.ID, Name: spec.Name, Image: spec.Image, State: StatePending, Health: "none", CreatedAt: time.Now(), Labels: labels}, nil
}

func toDockerResources(r Resources) container.Resources {
	out := container.Resources{}
	if r.CPUs > 0 {
		out.NanoCPUs = int64(r.CPUs * 1e9)
	}
	if r.MemoryMB > 0 {
		out.Memory = int64(r.MemoryMB) << 20
		out.MemorySwap = out.Memory // no swap: the limit is a hard wall
	}
	if r.PidsMax > 0 {
		p := int64(r.PidsMax)
		out.PidsLimit = &p
	}
	return out
}

func (d *DockerRuntime) Start(ctx context.Context, name string) error {
	if _, err := d.cli.ContainerStart(ctx, name, client.ContainerStartOptions{}); err != nil {
		return wrapNotFound(name, "start", err)
	}
	return nil
}

func (d *DockerRuntime) Stop(ctx context.Context, name string) error {
	t := 2
	if _, err := d.cli.ContainerStop(ctx, name, client.ContainerStopOptions{Timeout: &t}); err != nil {
		return wrapNotFound(name, "stop", err)
	}
	return nil
}

func (d *DockerRuntime) Kill(ctx context.Context, name string) error {
	if _, err := d.cli.ContainerKill(ctx, name, client.ContainerKillOptions{Signal: "SIGKILL"}); err != nil {
		if strings.Contains(err.Error(), "is not running") {
			return nil
		}
		return wrapNotFound(name, "kill", err)
	}
	return nil
}

func (d *DockerRuntime) Pause(ctx context.Context, name string) error {
	if _, err := d.cli.ContainerPause(ctx, name, client.ContainerPauseOptions{}); err != nil {
		return wrapNotFound(name, "pause", err)
	}
	return nil
}

func (d *DockerRuntime) Unpause(ctx context.Context, name string) error {
	if _, err := d.cli.ContainerUnpause(ctx, name, client.ContainerUnpauseOptions{}); err != nil {
		return wrapNotFound(name, "unpause", err)
	}
	return nil
}

func (d *DockerRuntime) Update(ctx context.Context, name string, r Resources) error {
	res := toDockerResources(r)
	if r.CPUs == 0 {
		res.NanoCPUs = 0 // 0 clears the quota on update
	}
	if _, err := d.cli.ContainerUpdate(ctx, name, client.ContainerUpdateOptions{Resources: &res}); err != nil {
		return wrapNotFound(name, "update", err)
	}
	return nil
}

func (d *DockerRuntime) Destroy(ctx context.Context, name string) error {
	if _, err := d.cli.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
		return wrapNotFound(name, "remove", err)
	}
	d.mu.Lock()
	delete(d.prevStats, name)
	d.mu.Unlock()
	return nil
}

func (d *DockerRuntime) Inspect(ctx context.Context, name string) (Container, error) {
	res, err := d.cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		return Container{}, wrapNotFound(name, "inspect", err)
	}
	return fromInspect(res.Container), nil
}

func fromInspect(c container.InspectResponse) Container {
	out := Container{ID: c.ID, Name: strings.TrimPrefix(c.Name, "/"), Health: "none"}
	if c.Config != nil {
		out.Image = c.Config.Image
		out.Labels = c.Config.Labels
	}
	out.CreatedAt, _ = time.Parse(time.RFC3339Nano, c.Created)
	if c.State != nil {
		out.ExitCode = c.State.ExitCode
		out.OOMKilled = c.State.OOMKilled
		out.Error = c.State.Error
		out.StartedAt, _ = time.Parse(time.RFC3339Nano, c.State.StartedAt)
		if c.State.Health != nil {
			out.Health = string(c.State.Health.Status)
		}
		out.State = mapState(c.State, out.Health)
		out.Running = c.State.Running || c.State.Paused || c.State.Restarting
	}
	if c.NetworkSettings != nil {
		for port, bindings := range c.NetworkSettings.Ports {
			if port.Num() == adminContainerPort && len(bindings) > 0 {
				out.AdminAddr = net.JoinHostPort(bindings[0].HostIP.String(), bindings[0].HostPort)
			}
		}
	}
	return out
}

// mapState folds Docker's status and health into the runtime State.
func mapState(s *container.State, health string) State {
	switch s.Status {
	case container.StateCreated:
		return StatePending
	case container.StatePaused:
		return StatePaused
	case container.StateRunning:
		switch health {
		case "healthy":
			return StateHealthy
		case "unhealthy":
			return StateFailed
		case "starting":
			return StateStarting
		}
		return StateHealthy // no healthcheck configured
	case container.StateRestarting:
		return StateStarting
	case container.StateExited, container.StateDead, container.StateRemoving:
		if s.ExitCode == 0 && !s.OOMKilled {
			return StateStopped
		}
		return StateFailed
	}
	return StateStopped
}

func (d *DockerRuntime) Stats(ctx context.Context, name string) (Stats, error) {
	res, err := d.cli.ContainerStats(ctx, name, client.ContainerStatsOptions{Stream: false})
	if err != nil {
		return Stats{}, wrapNotFound(name, "stats", err)
	}
	defer res.Body.Close()
	var cur container.StatsResponse
	if err := json.NewDecoder(res.Body).Decode(&cur); err != nil {
		return Stats{}, fmt.Errorf("stats %s: %w", name, err)
	}
	d.mu.Lock()
	prev, hadPrev := d.prevStats[name]
	d.prevStats[name] = cur
	d.mu.Unlock()

	st := Stats{SampledAt: cur.Read, MemoryLimit: cur.MemoryStats.Limit, ThrottledPeriods: cur.CPUStats.ThrottlingData.ThrottledPeriods}
	// usage minus the page cache, the way `docker stats` reports it
	st.MemoryBytes = cur.MemoryStats.Usage
	if cache, ok := cur.MemoryStats.Stats["inactive_file"]; ok && cache < st.MemoryBytes {
		st.MemoryBytes -= cache
	}
	for _, n := range cur.Networks {
		st.NetRxBytes += n.RxBytes
		st.NetTxBytes += n.TxBytes
	}
	if hadPrev {
		cpuDelta := float64(cur.CPUStats.CPUUsage.TotalUsage) - float64(prev.CPUStats.CPUUsage.TotalUsage)
		sysDelta := float64(cur.CPUStats.SystemUsage) - float64(prev.CPUStats.SystemUsage)
		if cpuDelta > 0 && sysDelta > 0 {
			cpus := float64(cur.CPUStats.OnlineCPUs)
			if cpus == 0 {
				cpus = float64(len(cur.CPUStats.CPUUsage.PercpuUsage))
			}
			if cpus == 0 {
				cpus = 1
			}
			st.CPUPercent = cpuDelta / sysDelta * cpus * 100
		}
	}
	return st, nil
}

func (d *DockerRuntime) Logs(ctx context.Context, name string, tail int) ([]string, error) {
	if tail <= 0 {
		tail = 200
	}
	res, err := d.cli.ContainerLogs(ctx, name, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Tail: fmt.Sprint(tail), Timestamps: false})
	if err != nil {
		return nil, wrapNotFound(name, "logs", err)
	}
	defer res.Close()
	pr, pw := io.Pipe()
	go func() {
		_, err := stdcopy.StdCopy(pw, pw, res)
		pw.CloseWithError(err)
	}()
	var lines []string
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines, nil
}

// FollowLogs streams one container's output until it stops or ctx ends.
// Only lines written after `since` are delivered, so a reconnect does not
// replay what the caller already has.
func (d *DockerRuntime) FollowLogs(ctx context.Context, name string, since time.Time, fn func(line string, stderr bool)) error {
	opts := client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Follow: true, Tail: "0"}
	if !since.IsZero() {
		opts.Since = since.UTC().Format(time.RFC3339Nano)
		opts.Tail = "all"
	}
	res, err := d.cli.ContainerLogs(ctx, name, opts)
	if err != nil {
		return wrapNotFound(name, "logs", err)
	}
	defer res.Close()

	// The stream multiplexes stdout and stderr; split it so the caller can
	// tell a panic from a log line.
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	var wg sync.WaitGroup
	wg.Add(2)
	go scanLines(outR, false, fn, &wg)
	go scanLines(errR, true, fn, &wg)
	_, copyErr := stdcopy.StdCopy(outW, errW, res)
	outW.CloseWithError(copyErr)
	errW.CloseWithError(copyErr)
	wg.Wait()
	if copyErr != nil && ctx.Err() == nil && !errors.Is(copyErr, io.EOF) {
		return fmt.Errorf("container logs %s: %w", name, copyErr)
	}
	return ctx.Err()
}

func scanLines(r io.Reader, stderr bool, fn func(string, bool), wg *sync.WaitGroup) {
	defer wg.Done()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			fn(line, stderr)
		}
	}
	_, _ = io.Copy(io.Discard, r) // drain, so StdCopy never blocks on a closed side
}

// ListHost reports every container on the machine the way `docker ps` does;
// all includes the stopped ones (`docker ps -a`).
func (d *DockerRuntime) ListHost(ctx context.Context, all bool) ([]HostContainer, error) {
	res, err := d.cli.ContainerList(ctx, client.ContainerListOptions{All: all})
	if err != nil {
		return nil, fmt.Errorf("container list: %w", err)
	}
	out := make([]HostContainer, 0, len(res.Items))
	for _, s := range res.Items {
		out = append(out, d.hostContainer(s))
	}
	// running first (that is what the tab is for), then newest first
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].State == "running") != (out[j].State == "running") {
			return out[i].State == "running"
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out, nil
}

func (d *DockerRuntime) hostContainer(s container.Summary) HostContainer {
	c := HostContainer{
		ID:         shortID(s.ID),
		Image:      s.Image,
		Command:    s.Command,
		CreatedAt:  s.Created * 1000,
		State:      string(s.State),
		Status:     s.Status,
		Ports:      portStrings(s.Ports),
		Ours:       s.Labels[LabelOwner] == d.owner,
		Deployment: s.Labels[LabelDeployment],
		Node:       s.Labels[LabelNode],
	}
	if len(s.Names) > 0 {
		c.Name = strings.TrimPrefix(s.Names[0], "/")
	}
	if s.Health != nil {
		c.Health = string(s.Health.Status)
	}
	if s.NetworkSettings != nil {
		for n := range s.NetworkSettings.Networks {
			c.Networks = append(c.Networks, n)
		}
		sort.Strings(c.Networks)
	}
	return c
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// portStrings renders the port mappings the way the CLI's PORTS column does.
func portStrings(ports []container.PortSummary) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		s := fmt.Sprintf("%d/%s", p.PrivatePort, p.Type)
		if p.PublicPort > 0 {
			host := "0.0.0.0"
			if p.IP.IsValid() {
				host = p.IP.String()
			}
			s = fmt.Sprintf("%s->%s", net.JoinHostPort(host, fmt.Sprint(p.PublicPort)), s)
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// List reports this instance's containers. The list endpoint already carries
// everything a running container needs, so it is one call for all of them;
// only a container that is no longer running is inspected, because its exit
// code and OOM flag — which tell a clean stop from a crash — are the one
// thing the summary leaves out.
func (d *DockerRuntime) List(ctx context.Context) ([]Container, error) {
	res, err := d.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: d.ownerFilter()})
	if err != nil {
		return nil, fmt.Errorf("container list: %w", err)
	}
	out := make([]Container, 0, len(res.Items))
	for _, s := range res.Items {
		c := fromSummary(s)
		if s.State != container.StateRunning && s.State != container.StatePaused {
			if full, err := d.Inspect(ctx, s.ID); err == nil {
				c = full
			}
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// fromSummary reads a container out of the list endpoint's row.
func fromSummary(s container.Summary) Container {
	c := Container{ID: s.ID, Image: s.Image, Labels: s.Labels, Health: "none", CreatedAt: time.Unix(s.Created, 0)}
	if len(s.Names) > 0 {
		c.Name = strings.TrimPrefix(s.Names[0], "/")
	}
	if s.Health != nil {
		c.Health = string(s.Health.Status)
	}
	c.State = mapSummaryState(s.State, c.Health)
	c.Running = s.State == container.StateRunning || s.State == container.StatePaused || s.State == container.StateRestarting
	c.AdminAddr = adminAddr(s.Ports)
	return c
}

// mapSummaryState folds the list endpoint's status and health into a State.
// The exited cases are the coarse reading; List inspects those for the exact
// one.
func mapSummaryState(s container.ContainerState, health string) State {
	switch s {
	case container.StateCreated:
		return StatePending
	case container.StatePaused:
		return StatePaused
	case container.StateRunning:
		switch health {
		case "healthy":
			return StateHealthy
		case "unhealthy":
			return StateFailed
		case "starting":
			return StateStarting
		}
		return StateHealthy // no healthcheck configured
	case container.StateRestarting:
		return StateStarting
	case container.StateExited, container.StateDead, container.StateRemoving:
		return StateStopped
	}
	return StateStopped
}

// adminAddr picks the host side of the published admin port, preferring the
// IPv4 binding the engine asks for over the daemon's IPv6 twin.
func adminAddr(ports []container.PortSummary) string {
	addr := ""
	for _, p := range ports {
		if p.PrivatePort != adminContainerPort || p.PublicPort == 0 {
			continue
		}
		host := "127.0.0.1"
		if p.IP.IsValid() {
			host = p.IP.String()
		}
		if addr == "" || p.IP.Is4() {
			addr = net.JoinHostPort(host, fmt.Sprint(p.PublicPort))
		}
		if p.IP.Is4() {
			break
		}
	}
	return addr
}

func (d *DockerRuntime) GC(ctx context.Context) (int, error) {
	n := 0
	res, err := d.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: d.ownerFilter()})
	if err != nil {
		return 0, fmt.Errorf("container list: %w", err)
	}
	for _, c := range res.Items {
		if _, err := d.cli.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err == nil {
			n++
		}
	}
	nets, err := d.cli.NetworkList(ctx, client.NetworkListOptions{Filters: d.ownerFilter()})
	if err != nil {
		return n, fmt.Errorf("network list: %w", err)
	}
	for _, nw := range nets.Items {
		if _, err := d.cli.NetworkRemove(ctx, nw.ID, client.NetworkRemoveOptions{}); err == nil {
			n++
		}
	}
	return n, nil
}

// --- events ----------------------------------------------------------------

// OnStateChange starts the event stream on first use and delivers a fresh
// Inspect for every container event that changes state.
func (d *DockerRuntime) OnStateChange(fn func(Container)) {
	d.mu.Lock()
	d.listeners = append(d.listeners, fn)
	if d.events == nil {
		ctx, cancel := context.WithCancel(context.Background())
		d.events = cancel
		go d.eventLoop(ctx)
	}
	d.mu.Unlock()
}

func (d *DockerRuntime) eventLoop(ctx context.Context) {
	filters := d.ownerFilter().Add("type", "container")
	for ctx.Err() == nil {
		res := d.cli.Events(ctx, client.EventsListOptions{Filters: filters})
	loop:
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-res.Messages:
				d.handleEvent(ctx, m)
			case err := <-res.Err:
				if err != nil && ctx.Err() == nil {
					time.Sleep(time.Second)
				}
				break loop
			}
		}
	}
}

func (d *DockerRuntime) handleEvent(ctx context.Context, m events.Message) {
	switch m.Action {
	case events.ActionStart, events.ActionDie, events.ActionPause, events.ActionUnPause, events.ActionKill,
		events.ActionOOM, events.ActionStop, events.ActionRestart, events.ActionDestroy:
	default:
		if !strings.HasPrefix(string(m.Action), "health_status") {
			return
		}
	}
	name := m.Actor.Attributes["name"]
	c, err := d.Inspect(ctx, m.Actor.ID)
	if err != nil {
		// destroyed: synthesise a stopped snapshot so the engine forgets it
		c = Container{ID: m.Actor.ID, Name: name, State: StateStopped, Labels: m.Actor.Attributes}
	}
	if m.Action == events.ActionOOM {
		c.OOMKilled = true
	}
	d.mu.Lock()
	ls := append([]func(Container){}, d.listeners...)
	d.mu.Unlock()
	for _, l := range ls {
		l(c)
	}
}

func wrapNotFound(name, op string, err error) error {
	if cerrdefs.IsNotFound(err) {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return fmt.Errorf("container %s %s: %w", op, name, err)
}
