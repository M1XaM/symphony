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

## How it runs

Every node kind is a prebuilt, universal container image (`src/blocks/`):

| block | kind | role |
|-------|------|------|
| `gateway`  | `infra`   | ingress + load generator; measures every saga end to end |
| `service`  | `service` | worker pool running a behaviour program per inbound edge; orchestrator, participant or choreography step depending on its links |
| `broker`   | `broker`  | topics, partitions, consumer groups, at-least-once push delivery |
| `database` | `db`      | bounded connection pool, latency model, transactions, storage cap |
| `proxy`    | `proxy`   | forwards paired edges; where link faults are injected |

Applying a topology creates a Docker network per deployment
(`symphony-<slug>`), one container per replica (`<slug>-<node>[-n]`) with
its wiring in `SYMPHONY_LINKS`, waits for each block's own `/health`, and
then drives it through the admin port every block publishes on
`127.0.0.1`: hot config (`config.set`), faults (`fault.set`, `chaos.*`), the
load generator (`load.set`), behaviour programs, logs and metrics. Container
limits (CPU, RAM), pause/kill and network partitions go through the Docker
Engine API directly. The contract is documented in `src/blocks/README.md`.

Block images are built by `make blocks`, or by the app itself from an
embedded copy of `src/blocks` when an image is missing, so a fresh install
needs only Docker. Without a reachable daemon the app falls back to an
in-memory fake runtime (`SYMPHONY_RUNTIME=fake` forces it) and mock numbers.

Containers keep running when the window closes and are adopted by the next
session (`SYMPHONY_TEARDOWN_ON_EXIT=1` tears them down instead). Everything
Symphony creates carries `symphony.owner=<instance id>` labels.

### Tests tab

Eighteen predefined scenarios (`src/engine/tests/scenarios/`) run a timeline
of load, faults, resource limits and config changes against the canvas
template, collect the same series the map shows, evaluate assertions per
phase and store the run under `<user config dir>/symphony/runs/`. Runs of the
same scenario on different templates can be compared side by side.

### Checks

    make test          # Go unit + blocks contract test (blocks run as processes, no Docker)
    make integration   # engine against the host's Docker daemon (real containers)
    make check         # go vet + client typecheck

## What is still simulated

| piece | today | where the real thing goes |
|-------|-------|---------------------------|
| Network-level shaping (bandwidth, netem) | done in the proxy block at HTTP level | tc/netem needs `NET_ADMIN`; add per-link if a scenario needs it on a link without a proxy |
| Third-party images | every node runs a Symphony block | a thin adapter block speaking the same contract in front of a real Postgres/Kafka |
| Metrics on the fake runtime | random walk derived from engine state | only used when Docker is unavailable |
