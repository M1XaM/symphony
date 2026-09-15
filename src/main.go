// Symphony desktop entry point.
//
// Wails v2 always compiles the module root as the application, and go:embed
// cannot reach outside a package's directory, so this file is where the
// built frontend and the block sources get embedded. All real wiring lives
// in engine/app. cmd/symphony is the equivalent entry for running from
// source.
package main

import (
	"embed"
	"log"

	"symphony/engine/app"
)

//go:embed all:client/dist
var assets embed.FS

// blocks is the source tree the block images are built from when they are
// missing on the machine: the module files plus src/blocks.
//
//go:embed go.mod go.sum all:blocks
var blocks embed.FS

func main() {
	if err := app.Run(app.Options{Assets: assets, Blocks: blocks}); err != nil {
		log.Fatal(err)
	}
}
