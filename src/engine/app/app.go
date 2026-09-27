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
	"symphony/engine/logstream"
	"symphony/engine/metrics"
	"symphony/engine/runtime"
	"symphony/engine/store"
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
	// TeardownOnExit removes every deployment when the window closes
	// (SYMPHONY_TEARDOWN_ON_EXIT=1); by default containers keep running and
	// are adopted by the next session.
	TeardownOnExit bool
	// DBPath is the state database; "" puts symphony.db next to the binary.
	DBPath string
}

// Run constructs bus -> runtime -> engine -> collectors -> api and blocks
// until the window closes.
func Run(opts Options) error {
	if opts.Title == "" {
		opts.Title = "Symphony"
	}
	if os.Getenv("SYMPHONY_TEARDOWN_ON_EXIT") == "1" {
		opts.TeardownOnExit = true
	}

	bus := events.NewBus()
	configDir := ""
	if dir, err := os.UserConfigDir(); err == nil {
		configDir = filepath.Join(dir, "symphony")
	} else {
		bus.Log("warn", "app", err.Error()+" — templates and test runs will not persist")
	}
	st := openStore(bus, opts.DBPath, configDir)
	owner, err := st.InstanceID()
	if err != nil {
		bus.Log("warn", "store", "instance id: "+err.Error())
		owner = "symphony"
	}

	docker, err := runtime.NewDocker(owner)
	if err != nil {
		return err
	}
	eng := core.New(core.Options{Runtime: docker, Bus: bus, Images: images.NewEnsurer(opts.Blocks), Store: st, FollowStore: true})
	col := metrics.NewCollector(bus, 500*time.Millisecond, eng)
	host := hoststats.New(bus, time.Second)
	runner := tests.NewRunner(eng, bus, configDir)
	conlogs := logstream.New(docker, bus)

	libPath, err := library.DefaultPath()
	if err != nil {
		bus.Log("warn", "library", err.Error()+" — saved templates will not persist")
	}
	bound := api.New(api.Deps{Engine: eng, Bus: bus, Library: library.New(libPath), Host: host, Tests: runner, Logs: conlogs})

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
			// Without a daemon the app still opens, and says so; it picks
			// Docker up (and what it left running) as soon as it answers.
			// The first look is made here, before the webview asks for
			// the deployments.
			dockerChanged := func(up bool, detail string) {
				if up {
					bus.Log("info", "runtime", "docker daemon connected ("+detail+")")
					eng.Restore(ctx)
				} else {
					bus.Log("warn", "runtime", detail)
				}
				api.SetRuntimeStatus(bound, events.RuntimeStatusPayload{Available: up, Detail: detail})
			}
			detail, err := docker.Ping(ctx)
			if err != nil {
				detail = err.Error()
			}
			dockerChanged(err == nil, detail)
			go docker.Watch(ctx, err == nil, dockerChanged)
			eng.Start(ctx)
			col.Start(ctx)
			host.Start(ctx)
			conlogs.Start(ctx)
		},
		OnShutdown: func(ctx context.Context) {
			if opts.TeardownOnExit {
				tctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				eng.TeardownAll(tctx)
				cancel()
			}
			conlogs.Stop()
			host.Stop()
			col.Stop()
			eng.Stop()
			api.Shutdown(bound)
			_ = docker.Close()
			_ = st.Close()
		},
		Bind: []interface{}{bound},
	})
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
