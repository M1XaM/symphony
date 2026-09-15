// Command symphony launches the app serving the frontend from
// ./client/dist on disk (relative to src/) instead of an embedded copy, and
// building block images from ./blocks on disk. Use it for
// `go run ./cmd/symphony` after `npm run build`; the shipped binary comes
// from `wails build` (see main.go at the module root and the Makefile).
package main

import (
	"log"
	"os"
	"path/filepath"

	"symphony/engine/app"
)

func main() {
	dist := "client/dist"
	if v := os.Getenv("SYMPHONY_DIST"); v != "" {
		dist = v
	}
	abs, err := filepath.Abs(dist)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(abs, "index.html")); err != nil {
		log.Fatalf("frontend not built: %s/index.html missing (run `make deps && cd src/client && npm run build`)", abs)
	}
	src, err := filepath.Abs(".")
	if err != nil {
		log.Fatal(err)
	}
	if err := app.Run(app.Options{Assets: os.DirFS(abs), Blocks: os.DirFS(src)}); err != nil {
		log.Fatal(err)
	}
}
