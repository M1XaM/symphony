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
| `make desktop`      | register `bin/symphony.desktop` with the desktop (Linux)      |

**What the container can target.** The output is a native binary for the
platform in `PLATFORM` (default `linux/amd64`). It is linked against
glibc/GTK3/WebKit2GTK 4.1 from Debian bookworm, so it runs on any recent
Linux desktop with `libgtk-3-0` and `libwebkit2gtk-4.1-0` installed.
`make PLATFORM=windows/amd64` cross-compiles a Windows binary
(`bin/install.exe`) from the same container. macOS cannot be cross-compiled
without Apple's SDK; build natively on a Mac with `make build-local`.

**Launching without a terminal.** `bin/install` is a plain GUI binary and
never needs a console. On Linux the build also writes `bin/symphony.desktop`
(`Terminal=false`); `make desktop` copies it to
`~/.local/share/applications`, after which Symphony appears in the app grid
and file managers open the binary as an application rather than wrapping an
unknown executable in a terminal window.

### Native build (optional)

If you would rather not use Docker, install Go 1.27+, Node 22+, and the Wails
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
| Link health | The map colours every edge of a running topology from a per-edge error rate (`link:health`, every 500 ms): green below 5 % lost requests, yellow from 5 %, red from 25 % (`metrics.WarnThreshold` / `CritThreshold`). Today the rate is derived from the endpoint nodes' state and active faults. | Replace `Ticker.errorRate` with proxy / consumer counters; keep `events.LinkHealthPayload`. |
| Host stats | Real: `src/engine/hoststats` samples the machine's CPU, RAM and network throughput once a second with gopsutil and feeds the Stats tab. | — |
| Deployments | Real, in memory: every applied topology is a deployment keyed by its name with its own network (`symphony-<slug>`), container names (`<slug>-<node>`), statuses and faults, so several templates run side by side and each is applied, tuned and torn down on its own. | `runtime.DockerRuntime` gives each deployment real containers on its own network. |
| Persistence | Real for templates: `engine/library` keeps the ones the user builds in `<user config dir>/symphony/templates.json`, mirrored into the webview's `localStorage` as a fallback. Built-in templates stay Go literals in `src/engine/spec/presets.go`. | The applied topology and run history are still not saved; `spec.Encode/Decode` and `library.Library` are what to build that on. |
| Edge editing | Edges are created by connecting handles; kind is inferred, topic defaults to `<node>.events`. A proxy node can sit in any path (`service -> proxy -> service`, `broker -> proxy -> service`); messaging edges through it are resolved to the broker behind it. | Inspector panel for edges. |

Not in scope here by design: tests, Docker calls, packaging beyond `make build`.
