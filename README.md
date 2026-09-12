# Symphony

Desktop tool for benchmarking **orchestration vs. choreography** in microservice
systems: describe a topology as a graph, apply it to a container runtime, inject
faults, and watch the transient behaviour.

## Build and run

The default build runs inside a Docker container, so the host needs only
**Docker** and **make**.

    $make
    $./bin/install  # or: make run

| target              | does                                                          |
|---------------------|---------------------------------------------------------------|
| `make` / `make build` | `wails build` in the container, then copy to `./bin/install` |
| `make image`        | (re)build the `symphony-builder` image from `src/build/docker` |
| `make shell`        | interactive shell inside the builder container                |
| `make run`          | build, then launch `./bin/install`                            |
| `make clean`        | remove `bin/`, `src/build/bin/`, `src/client/dist/`           |
| `make clean-docker` | `clean` plus remove the image and cache volumes               |

**What the container can target.** The output is a native binary for the
platform in `PLATFORM` (default `linux/amd64`). It is linked against
glibc/GTK3/WebKit2GTK 4.1 from Debian bookworm, so it runs on any recent
Linux desktop with `libgtk-3-0` and `libwebkit2gtk-4.1-0` installed.
`make PLATFORM=windows/amd64` cross-compiles a Windows binary
(`bin/install.exe`) from the same container. macOS cannot be cross-compiled
without Apple's SDK; build natively on a Mac with `make build-local`.

### Native build (optional)

If you would rather not use Docker, install Go 1.25+, Node 20+, and the Wails
platform dependencies. On Debian/Ubuntu:

    sudo apt install gcc pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev

Then:

    make deps         # go mod tidy, npm install, install the wails CLI if absent
    make build-local  # wails build on the host -> ./bin/install
    make dev          # wails dev with hot reload (native only: needs a display)
    make check        # go vet + client typecheck
    make bindings     # regenerate src/client/wailsjs after changing the Go API

On Linux the Makefile adds `-tags webkit2_41` automatically when only
webkit2gtk-4.1 is installed. On macOS `./bin/install` is a launcher script
next to the copied `.app` bundle.

## What is stubbed

| piece | today | where the real thing goes |
|-------|-------|---------------------------|
| Container runtime | `runtime.FakeRuntime` walks containers `pending → starting → healthy` on timers. | `src/engine/runtime/docker.go`: every method returns `ErrNotImplemented`; implement with `github.com/docker/docker/client` and swap the constructor in `src/engine/app/app.go`. |
| Topics | `ensure_topic` sleeps 80 ms and logs. | `core.Engine.executeOp` case `OpEnsureTopic`: broker admin client. |
| Hot config transport | `control` ops and `SetHotConfig` update the engine's copy and log. | `core.Engine.executeOp` case `OpControl` and `core.Engine.SetHotConfig`: HTTP admin endpoint / SIGHUP / toxiproxy API. |
| Faults | `pause` and `kill` go through the runtime; `latency`, `drop`, `partition`, `cpu_stress` are recorded and logged only. | `core.Engine.InjectFault`: toxiproxy toxics, netem, stress-ng exec. |
| Metrics | `src/engine/metrics` random-walks around a baseline that reacts to node health and active faults. | Replace `Ticker.sample` with real collectors; keep `events.MetricsPayload`. |
| Persistence | Real for templates: `engine/library` keeps the ones the user builds in `<user config dir>/symphony/templates.json`, mirrored into the webview's `localStorage` as a fallback. Built-in templates stay Go literals in `src/engine/spec/presets.go`. | The applied topology and run history are still not saved; `spec.Encode/Decode` and `library.Library` are what to build that on. |
| Edge editing | Edges are created by connecting handles; kind is inferred, topic defaults to `<node>.events`. | Inspector panel for edges. |

Not in scope here by design: tests, Docker calls, packaging beyond `make build`.
