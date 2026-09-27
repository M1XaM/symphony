//go:build cli

// symphony-cli entry point: the same package built with `-tags cli` is a
// terminal program driving the same engine and state as the window (see
// engine/cli). It needs no webview, no frontend and no cgo:
//
//	CGO_ENABLED=0 go build -tags cli -o symphony-cli .
package main

import (
	"os"

	"symphony/engine/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], blocks))
}
