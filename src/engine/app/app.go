// Package app wires the backend together and runs the Wails window. It is
// shared by the two entry points (main.go at the module root, which Wails
// builds with the frontend embedded, and cmd/symphony, which serves the
// frontend from disk for go-run development).
package app

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/logger"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"symphony/engine/api"
	"symphony/engine/core"
	"symphony/engine/events"
	"symphony/engine/library"
	"symphony/engine/metrics"
	"symphony/engine/runtime"
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
	Title  string
}

// Run constructs bus -> runtime -> engine -> metrics -> api and blocks until
// the window closes.
func Run(opts Options) error {
	if opts.Title == "" {
		opts.Title = "Symphony"
	}

	bus := events.NewBus()
	rt := runtime.NewFake(runtime.FakeOptions{})
	// Swap for runtime.NewDocker("") once DockerRuntime is implemented.
	eng := core.New(rt, bus)
	tick := metrics.NewTicker(bus, 500*time.Millisecond, eng.MetricInputs)

	libPath, err := library.DefaultPath()
	if err != nil {
		bus.Log("warn", "library", err.Error()+" — saved templates will not persist")
	}
	bound := api.New(eng, bus, library.New(libPath))

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
			tick.Start(ctx)
		},
		OnShutdown: func(ctx context.Context) {
			tick.Stop()
			api.Shutdown(bound)
		},
		Bind: []interface{}{bound},
	})
}
