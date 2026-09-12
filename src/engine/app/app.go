// Package app wires the backend together and runs the Wails window. It is
// shared by the two entry points (main.go at the module root, which Wails
// builds with the frontend embedded, and cmd/symphony, which serves the
// frontend from disk for go-run development).
package app

import (
	"context"
	"io/fs"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"symphony/engine/api"
	"symphony/engine/core"
	"symphony/engine/events"
	"symphony/engine/metrics"
	"symphony/engine/runtime"
)

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
	bound := api.New(eng, bus)

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
