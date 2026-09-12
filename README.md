# Symphony

Desktop tool for benchmarking **orchestration vs. choreography** in microservice
systems: describe a topology as a graph, apply it to a container runtime, inject
faults, and watch the transient behaviour.

**Status: scaffold.** The app compiles, launches, and drives the whole
validate → diff → plan → apply pipeline against an in-memory fake runtime. No
Docker daemon is required. See [What is stubbed](#what-is-stubbed) for the
seams where the real implementation goes.

## Build and run

The default build runs inside a Docker container, so the host needs only
**Docker** and **make** (on Windows: Docker Desktop plus make from Git Bash,
MSYS2 or WSL). No Go, Node, gcc or GTK/WebKit headers on the host.

    make           # builds the builder image once, then builds -> ./bin/install
    ./bin/install  # or: make run

The first `make image` downloads the Go and Node base images and the WebKit
dev packages (a few minutes, ~2 GB). Later builds reuse the image and keep Go
modules, the Go build cache, the npm cache and the container's `node_modules`
in named Docker volumes, so an incremental build is well under a minute.

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

## Layout

Everything code-related lives under `src/`, which is also the Wails project
root and the Go module root. The repo root holds only the Makefile, README,
`.gitignore`, and build output in `bin/`.

    Makefile                    build targets; all of them cd into src/
    README.md
    src/
      go.mod                    module "symphony"
      main.go                   Wails entry: embeds client/dist, calls engine/app.Run
      wails.json                frontend:dir = ./client
      build/                    Wails platform assets (icons, plists, installer)
        docker/                 Builder image (Dockerfile) and the in-container build script
      cmd/symphony/main.go      Same wiring, client served from disk (`go run ./cmd/symphony` from src/)
      engine/                   Go backend
        spec/                   Topology domain model, JSON, Validate(), presets, FaultSpec
        planner/                Diff(current, desired) -> Plan of ordered Operations,
                                each classified hot | cold | structural
        runtime/                Runtime interface, FakeRuntime (in-memory), DockerRuntime stub
        core/                   validate -> diff -> plan -> execute; node status; hot config; faults
        events/                 Typed event bus + payload structs
        metrics/                Mock 500 ms metrics ticker
        api/                    Wails-bound API struct; forwards bus events to the webview
        app/                    Constructs everything and runs the Wails window
      client/                   React 18 + TypeScript + Vite frontend
        src/types/              TS mirror of the domain model
        src/store/              Zustand store (topology, statuses, plan, log, metrics)
        src/lib/                Binding wrapper, event subscription, formatting
        src/components/         Palette, Canvas (React Flow), Toolbar, Inspector, Drawer, chart
        wailsjs/                Generated bindings (do not edit; `make bindings`)

Why two `main.go` files: Wails v2 always compiles the project root, and
`go:embed` cannot reach outside a package directory, so the embedding entry
has to live at `src/`. `cmd/symphony` keeps the conventional layout for
from-source runs. Both are a dozen lines calling `engine/app.Run`.

### Bound API (frontend → Go)

| method                          | notes                                              |
|---------------------------------|----------------------------------------------------|
| `GetTopology()`                 | currently applied topology                          |
| `GetPresets()`                  | the two seeded saga topologies                      |
| `GetPhase()`                    | engine phase for the status pill                    |
| `ValidateTopology(t)`           | all findings, errors and warnings                   |
| `PlanApply(t)`                  | plan without executing; rejects on validation error |
| `Apply(t)`                      | executes; streams `apply:progress` events           |
| `Teardown()`                    | destroys everything running                         |
| `SetHotConfig(nodeID, patch)`   | immediate `control` op on a running node            |
| `InjectFault(nodeID, fault)`    | see stubs below                                     |
| `GetNodeStatus()`               | map of node id → status                             |

### Events (Go → frontend)

`apply:progress`, `engine:log`, `engine:phase`, `node:status`, `metrics:tick`,
`fault:injected`. Payload shapes live in `src/engine/events/bus.go` and are
mirrored in `src/client/src/types/topology.ts`.

## What is stubbed

| piece | today | where the real thing goes |
|-------|-------|---------------------------|
| Container runtime | `runtime.FakeRuntime` walks containers `pending → starting → healthy` on timers. | `src/engine/runtime/docker.go`: every method returns `ErrNotImplemented`; implement with `github.com/docker/docker/client` and swap the constructor in `src/engine/app/app.go`. |
| Topics | `ensure_topic` sleeps 80 ms and logs. | `core.Engine.executeOp` case `OpEnsureTopic`: broker admin client. |
| Hot config transport | `control` ops and `SetHotConfig` update the engine's copy and log. | `core.Engine.executeOp` case `OpControl` and `core.Engine.SetHotConfig`: HTTP admin endpoint / SIGHUP / toxiproxy API. |
| Faults | `pause` and `kill` go through the runtime; `latency`, `drop`, `partition`, `cpu_stress` are recorded and logged only. | `core.Engine.InjectFault`: toxiproxy toxics, netem, stress-ng exec. |
| Metrics | `src/engine/metrics` random-walks around a baseline that reacts to node health and active faults. | Replace `Ticker.sample` with real collectors; keep `events.MetricsPayload`. |
| Persistence | Presets are Go literals in `src/engine/spec/presets.go`; nothing is written to disk. | `spec.Encode/Decode` already exist; add load/save bound methods. |
| Edge editing | Edges are created by connecting handles; kind is inferred, topic defaults to `<node>.events`. | Inspector panel for edges. |

Not in scope here by design: tests, Docker calls, packaging beyond `make build`.
