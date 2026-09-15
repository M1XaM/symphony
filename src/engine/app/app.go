// Package app wires the backend together and runs the Wails window. It is
// shared by the two entry points (main.go at the module root, which Wails
// builds with the frontend and the block sources embedded, and cmd/symphony,
// which serves both from disk for go-run development).
package app

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/logger"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"symphony/engine/api"
	"symphony/engine/core"
	"symphony/engine/events"
	"symphony/engine/hoststats"
	"symphony/engine/images"
	"symphony/engine/library"
	"symphony/engine/metrics"
	"symphony/engine/runtime"
	"symphony/engine/tests"
)

// busLogger routes Wails' own logging into the event bus, so it lands in the
// app's Log tab rather than on a stdout nobody is watching: the app is a GUI
// binary and is not meant to need a terminal. Errors are echoed to stderr as
// well, because if the window never opens that is the only place left to look.
type busLogger struct{ bus *events.Bus }

func (l busLogger) Print(m string)   { l.bus.Log("info", "wails", m) }
func (l busLogger) Trace(m string)   { l.bus.Log("debug", "wails", m) }
func (l busLogger) Debug(m string)   { l.bus.Log("debug", "wails", m) }
func (l busLogger) Info(m string)    { l.bus.Log("info", "wails", m) }
func (l busLogger) Warning(m string) { l.bus.Log("warn", "wails", m) }

func (l busLogger) Error(m string) {
	l.bus.Log("error", "wails", m)
	fmt.Fprintln(os.Stderr, "error: "+m)
}

// Fatal must not return: the framework calls it in place of exiting itself.
func (l busLogger) Fatal(m string) {
	l.bus.Log("error", "wails", m)
	fmt.Fprintln(os.Stderr, "fatal: "+m)
	os.Exit(1)
}

// Options for Run.
type Options struct {
	Assets fs.FS
	// Blocks is the source tree the block images are built from (go.mod,
	// go.sum and blocks/); nil disables in-app image builds.
	Blocks fs.FS
	Title  string
	// ForceFake skips Docker even when a daemon is reachable (SYMPHONY_RUNTIME=fake).
	ForceFake bool
	// TeardownOnExit removes every deployment when the window closes
	// (SYMPHONY_TEARDOWN_ON_EXIT=1); by default containers keep running and
	// are adopted by the next session.
	TeardownOnExit bool
}

// Run constructs bus -> runtime -> engine -> collectors -> api and blocks
// until the window closes.
func Run(opts Options) error {
	if opts.Title == "" {
		opts.Title = "Symphony"
	}
	if os.Getenv("SYMPHONY_RUNTIME") == "fake" {
		opts.ForceFake = true
	}
	if os.Getenv("SYMPHONY_TEARDOWN_ON_EXIT") == "1" {
		opts.TeardownOnExit = true
	}

	bus := events.NewBus()
	configDir := ""
	if dir, err := os.UserConfigDir(); err == nil {
		configDir = filepath.Join(dir, "symphony")
	} else {
		bus.Log("warn", "app", err.Error()+" — templates, deployments and test runs will not persist")
	}
	store := core.NewStore(configDir)
	owner, err := store.InstanceID()
	if err != nil {
		bus.Log("warn", "app", "instance id: "+err.Error())
		owner = "symphony"
	}

	var rt runtime.Runtime
	info := api.RuntimeInfo{Kind: "fake", Detail: "no container daemon; containers are simulated"}
	var dockerRT *runtime.DockerRuntime
	if !opts.ForceFake {
		d, detail, err := runtime.NewDocker(context.Background(), owner)
		if err != nil {
			bus.Log("warn", "runtime", err.Error()+" — falling back to the fake runtime")
		} else {
			dockerRT = d
			rt = d
			info = api.RuntimeInfo{Kind: "docker", Detail: detail}
		}
	}
	if rt == nil {
		rt = runtime.NewFake(runtime.FakeOptions{})
	}

	eng := core.New(core.Options{Runtime: rt, Bus: bus, Images: images.NewEnsurer(opts.Blocks), Store: store})
	col := metrics.NewCollector(bus, 500*time.Millisecond, eng)
	host := hoststats.New(bus, time.Second)
	runner := tests.NewRunner(eng, bus, configDir, info.Kind)

	libPath, err := library.DefaultPath()
	if err != nil {
		bus.Log("warn", "library", err.Error()+" — saved templates will not persist")
	}
	bound := api.New(api.Deps{Engine: eng, Bus: bus, Library: library.New(libPath), Host: host, Collector: col, Tests: runner, Runtime: info})

	return wails.Run(&options.App{
		Title:     opts.Title,
		Width:     1440,
		Height:    900,
		MinWidth:  1024,
		MinHeight: 640,
		AssetServer: &assetserver.Options{
			Assets: opts.Assets,
		},
		BackgroundColour: &options.RGBA{R: 0x12, G: 0x13, B: 0x16, A: 1},
		Logger:           busLogger{bus: bus},
		// same level either way, so a release build's Log tab says as much as
		// a development one's
		LogLevel:           logger.INFO,
		LogLevelProduction: logger.INFO,
		OnStartup: func(ctx context.Context) {
			api.Startup(bound, ctx)
			if dockerRT != nil {
				eng.Adopt(ctx)
			}
			eng.Start(ctx)
			col.Start(ctx)
			host.Start(ctx)
		},
		OnShutdown: func(ctx context.Context) {
			if opts.TeardownOnExit {
				tctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				eng.TeardownAll(tctx)
				cancel()
			}
			host.Stop()
			col.Stop()
			eng.Stop()
			api.Shutdown(bound)
			if dockerRT != nil {
				_ = dockerRT.Close()
			}
		},
		Bind: []interface{}{bound},
	})
}
