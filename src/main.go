//go:build !cli

// Symphony desktop entry point.
//
// Wails v2 always compiles the module root as the application, and go:embed
// cannot reach outside a package's directory, so this file is where the
// built frontend gets embedded (the block sources are in blocks.go). All
// real wiring lives in engine/app. cmd/symphony is the equivalent entry for
// running from source. Built with -tags cli, this package is symphony-cli
// instead (main_cli.go).
package main

import (
	"embed"
	"log"

	"symphony/engine/app"
)

//go:embed all:client/dist
var assets embed.FS

func main() {
	if err := app.Run(app.Options{Assets: assets, Blocks: blocks}); err != nil {
		log.Fatal(err)
	}
}
