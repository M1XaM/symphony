package main

import "embed"

// blocks is the source tree the block images are built from when they are
// missing on the machine: the module files plus src/blocks. Both the window
// and symphony-cli carry it.
//
//go:embed go.mod go.sum all:blocks
var blocks embed.FS
