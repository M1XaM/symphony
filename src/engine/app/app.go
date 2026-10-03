// Package app runs the desktop window: the backend's services (see
// engine/backend) behind a Wails webview. It is shared by the two desktop
// entry points (main.go at the module root, which Wails builds with the
// frontend and the block sources embedded, and cmd/symphony, which serves
// both from disk for go-run development).
package app

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/logger"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsrt "github.com/wailsapp/wails/v2/pkg/runtime"

	"symphony/engine/api"
	"symphony/engine/backend"
	"symphony/engine/events"
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

// Run starts the backend and blocks until the window closes.
func Run(opts Options) error {
	if opts.Title == "" {
		opts.Title = "Symphony"
	}
	if os.Getenv("SYMPHONY_TEARDOWN_ON_EXIT") == "1" {
		opts.TeardownOnExit = true
	}
	b, err := backend.New(backend.Options{Blocks: opts.Blocks, DBPath: opts.DBPath, FollowStore: true})
	if err != nil {
		return err
	}

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
		Logger:           busLogger{bus: b.Bus},
		// same level either way, so a release build's Log tab says as much as
		// a development one's
		LogLevel:           logger.INFO,
		LogLevelProduction: logger.INFO,
		OnStartup: func(ctx context.Context) {
			api.SetSaveDialog(b.API, func(name, format string) (string, error) {
				return wailsrt.SaveFileDialog(ctx, wailsrt.SaveDialogOptions{
					Title:           "Save as",
					DefaultFilename: name,
					Filters:         []wailsrt.FileFilter{{DisplayName: strings.ToUpper(format) + " (*." + format + ")", Pattern: "*." + format}},
				})
			})
			api.SetOpenDialog(b.API, func(format string) (string, error) {
				return wailsrt.OpenFileDialog(ctx, wailsrt.OpenDialogOptions{
					Title:   "Open",
					Filters: []wailsrt.FileFilter{{DisplayName: strings.ToUpper(format) + " (*." + format + ")", Pattern: "*." + format}},
				})
			})
			b.Start(ctx, func(name string, payload any) { wailsrt.EventsEmit(ctx, name, payload) })
		},
		OnShutdown: func(ctx context.Context) {
			if opts.TeardownOnExit {
				tctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				b.Engine.TeardownAll(tctx)
				cancel()
			}
			b.Close()
		},
		Bind: []interface{}{b.API},
	})
}
