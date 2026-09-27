// Package backend assembles Symphony's services (event bus, state store,
// Docker runtime, engine, metrics, host stats, tests, container log mirror
// and the API over them) for its two front ends: the desktop app
// (engine/app) and the command line (engine/cli). Both drive the system
// through the same api.API, on the same state database.
package backend

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"symphony/engine/api"
	"symphony/engine/core"
	"symphony/engine/events"
	"symphony/engine/hoststats"
	"symphony/engine/images"
	"symphony/engine/library"
	"symphony/engine/logstream"
	"symphony/engine/metrics"
	"symphony/engine/runtime"
	"symphony/engine/store"
	"symphony/engine/tests"
)

// Options for New.
type Options struct {
	// Blocks is the source tree the block images are built from (go.mod,
	// go.sum and blocks/); nil disables building them.
	Blocks fs.FS
	// DBPath is the state database; "" puts symphony.db next to the binary.
	DBPath string
	// FollowStore picks up what other processes change in the database;
	// the desktop app sets it so it sees what the CLI does.
	FollowStore bool
}

// Backend is every service of one Symphony process.
type Backend struct {
	Bus     *events.Bus
	Store   *store.Store
	Docker  *runtime.DockerRuntime
	Engine  *core.Engine
	Metrics *metrics.Collector
	Host    *hoststats.Sampler
	Tests   *tests.Runner
	Logs    *logstream.Streamer
	Library *library.Library
	API     *api.API
}

// New builds the services. Nothing runs and nothing talks to Docker yet.
func New(opts Options) (*Backend, error) {
	b := &Backend{Bus: events.NewBus()}
	configDir := ""
	if dir, err := os.UserConfigDir(); err == nil {
		configDir = filepath.Join(dir, "symphony")
	} else {
		b.Bus.Log("warn", "app", err.Error()+" — templates and test runs will not persist")
	}
	b.Store = openStore(b.Bus, opts.DBPath, configDir)
	owner, err := b.Store.InstanceID()
	if err != nil {
		b.Bus.Log("warn", "store", "instance id: "+err.Error())
		owner = "symphony"
	}
	if b.Docker, err = runtime.NewDocker(owner); err != nil {
		b.Store.Close()
		return nil, err
	}
	b.Engine = core.New(core.Options{Runtime: b.Docker, Bus: b.Bus, Images: images.NewEnsurer(opts.Blocks), Store: b.Store, FollowStore: opts.FollowStore})
	b.Metrics = metrics.NewCollector(b.Bus, 500*time.Millisecond, b.Engine)
	b.Host = hoststats.New(b.Bus, time.Second)
	b.Tests = tests.NewRunner(b.Engine, b.Bus, configDir)
	b.Logs = logstream.New(b.Docker, b.Bus)
	libPath := ""
	if configDir != "" {
		libPath = filepath.Join(configDir, library.FileName)
	}
	b.Library = library.New(libPath)
	b.API = api.New(api.Deps{Engine: b.Engine, Bus: b.Bus, Library: b.Library, Host: b.Host, Tests: b.Tests, Logs: b.Logs})
	return b, nil
}

// Connect asks whether the Docker daemon answers, tells the API (and so the
// front end), and when it does makes the stored deployments live: restore
// reopens broken ones and removes orphans (the desktop app), otherwise they
// are only attached as they are (a CLI command).
func (b *Backend) Connect(ctx context.Context, restore bool) bool {
	detail, err := b.Docker.Ping(ctx)
	if err != nil {
		detail = err.Error()
	}
	b.dockerChanged(ctx, err == nil, detail, restore)
	return err == nil
}

func (b *Backend) dockerChanged(ctx context.Context, up bool, detail string, restore bool) {
	switch {
	case !up:
		b.Bus.Log("warn", "runtime", detail)
	case restore:
		b.Bus.Log("info", "runtime", "docker daemon connected ("+detail+")")
		b.Engine.Restore(ctx)
	default:
		if _, err := b.Engine.Attach(ctx); err != nil {
			b.Bus.Log("warn", "engine", "could not read the stored deployments: "+err.Error())
		}
	}
	api.SetRuntimeStatus(b.API, events.RuntimeStatusPayload{Available: up, Detail: detail})
}

// Start runs everything the desktop app keeps running for its session: the
// Docker watcher (without a daemon the app still opens, says so, and picks
// Docker and the stored deployments up as soon as it answers), the engine's
// health poller, metrics, host stats and the container log mirror. emit
// receives every event.
func (b *Backend) Start(ctx context.Context, emit func(name string, payload any)) {
	api.Startup(b.API, ctx, emit)
	up := b.Connect(ctx, true)
	go b.Docker.Watch(ctx, up, func(up bool, detail string) { b.dockerChanged(ctx, up, detail, true) })
	b.Engine.Start(ctx)
	b.Metrics.Start(ctx)
	b.Host.Start(ctx)
	b.Logs.Start(ctx)
}

// Close stops whatever runs and releases Docker and the database.
func (b *Backend) Close() {
	b.Logs.Stop()
	b.Host.Stop()
	b.Metrics.Stop()
	b.Engine.Stop()
	api.Shutdown(b.API)
	_ = b.Docker.Close()
	_ = b.Store.Close()
}

// openStore opens the state database next to the binary, or where path
// says. When that directory is not writable (a binary installed under
// /usr/bin, say) it falls back to the user config directory; with neither,
// nothing persists and the app still runs.
func openStore(bus *events.Bus, path, configDir string) *store.Store {
	if path == "" {
		p, err := store.DefaultPath()
		if err != nil {
			bus.Log("warn", "store", err.Error())
		}
		path = p
	}
	var st *store.Store
	var err error
	if path != "" {
		st, err = store.Open(path)
	}
	if st == nil && configDir != "" {
		fallback := filepath.Join(configDir, store.FileName)
		bus.Log("warn", "store", fmt.Sprintf("%v — using %s instead", err, fallback))
		st, err = store.Open(fallback)
	}
	if st == nil {
		bus.Log("error", "store", fmt.Sprintf("%v — deployments will not be remembered", err))
		return nil
	}
	bus.Log("info", "store", "state in "+st.Path())
	if n, err := st.ImportLegacy(configDir); err != nil {
		bus.Log("warn", "store", "importing the old deployment records: "+err.Error())
	} else if n > 0 {
		bus.Log("info", "store", fmt.Sprintf("imported %d deployment(s) from %s", n, configDir))
	}
	return st
}
