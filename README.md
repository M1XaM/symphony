# Symphony

Desktop tool for benchmarking **orchestration vs. choreography** in microservice
systems: draw a topology as a graph, run it as real Docker containers, drive
load through it, inject faults, and watch how each coordination style behaves
under stress. Predefined test scenarios make the two styles comparable run for
run.

## Download

Every `vX.Y.Z` tag publishes a release with two files (plus `SHA256SUMS`):

| file | platform |
|------|----------|
| `symphony-vX.Y.Z-linux-amd64`       | Linux x86-64 |
| `symphony-vX.Y.Z-windows-amd64.exe` | Windows 10/11 x86-64 |

**Linux.** The binary needs glibc 2.34+ (Ubuntu 22.04, Debian 12, Fedora 35 or
newer) with GTK 3 and WebKitGTK 4.1:

    sudo apt install libgtk-3-0 libwebkit2gtk-4.1-0   # Debian / Ubuntu
    chmod +x symphony-vX.Y.Z-linux-amd64
    ./symphony-vX.Y.Z-linux-amd64                    # or double-click it

It is a GUI program and never opens a terminal. Keep a name like `symphony`
if you rename it: a file called `install` is taken for an INSTALL text file by
the desktop, which then opens it as a script.

**Windows.** Run the `.exe`; nothing to install. It uses the WebView2 runtime
that ships with Windows 10/11 (the app offers to fetch it if it is missing).

**Docker** (Docker Engine on Linux, Docker Desktop on Windows) is what the
topologies run on. The first Apply builds the five block images from a copy of
their sources embedded in the app, which takes a minute or two; later runs
reuse them. Without a reachable Docker daemon Symphony still starts and runs
everything against an in-memory simulation with mock numbers.

## Using it

The map fills the window and the panels float over it.

- **Toolbar**: the topology's name, the runtime in use (`docker` or
  `simulated`), and **Validate**, **Plan**, **Apply**, **Teardown** for the
  topology on the map, with its phase on the right.
- **Left panel**: **Elements** to drag onto the map (service, broker,
  database, proxy, gateway) and **Templates**: the two built-in sagas
  (*Orchestration* and *Choreography*) plus the ones you save with **Add
  template**. A dot marks every template the engine is running; several run
  side by side, each on its own network.
- **Map**: drag between handles to connect nodes (the edge kind is inferred),
  Delete removes the selection. Once applied, edges turn green, yellow or red
  with their error rate, and edges carrying traffic animate.
- **Right panel (inspector)**: with nothing selected, a run summary (sagas/s,
  p99, failure rate, consumer lag, CPU, active faults). A selected node shows
  its deep health checks, live CPU/RAM limits, hot config (applied to the
  running container at once), cold config (applied on the next Apply), fault
  buttons and, on the gateway, the load generator. A selected edge shows its
  rate and latency and faults scoped to that one link.
- **Drawer** (bottom):

  | tab | shows |
  |-----|-------|
  | Plan       | validation findings and the operations Apply would run, classed hot / cold / structural |
  | Stats      | host CPU, RAM and network, and the running simulation's container totals and per-node usage |
  | Containers | `watch docker ps`: every container on the machine, the ones Symphony started marked |
  | Node logs  | the container logs of one node |
  | Log        | the engine's own events and, with the `docker logs` switch on, every line the containers print; filterable |
  | Tests      | the scenario catalogue, runs, and side-by-side comparison |

Either side panel folds away with the arrow in the middle of its inner edge.

### Tests

Eighteen predefined scenarios (`src/engine/tests/scenarios/`: baseline, ramp,
burst, crash, link latency and loss, partition, central outage, pool
exhaustion, CPU throttle, OOM, slow consumer, retry storm, hot config, scale
out, duplicates, chaos soak, cold start) run a timeline of load, faults,
resource limits and config changes against the template on the map. Each run
collects the same series the map shows, evaluates assertions per phase and is
stored, so runs of one scenario on the two templates can be compared.

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

Containers keep running when the window closes and are adopted by the next
session. Everything Symphony creates carries `symphony.owner=<instance id>`
labels, so it never touches other containers.

### Settings and files

| environment variable | effect |
|----------------------|--------|
| `SYMPHONY_RUNTIME=fake`        | use the in-memory simulation even when Docker is available |
| `SYMPHONY_TEARDOWN_ON_EXIT=1`  | tear every deployment down when the window closes |

State lives in the user config directory (`~/.config/symphony` on Linux,
`%AppData%\symphony` on Windows): `templates.json` (saved templates),
`deployments/` (what to adopt on the next start), `runs/` (test results) and
`instance` (the owner id on the labels).

## Build from source

The build runs inside a Docker container, so the host needs only **Docker**
and **make**:

    make                # -> ./bin/symphony
    ./bin/symphony      # or: make run

| target | does |
|--------|------|
| `make` / `make build` | `wails build` in the container, then copy to `./bin/symphony` |
| `make run`            | build, then launch `./bin/symphony` |
| `make desktop`        | register `bin/symphony.desktop` with the desktop, so Symphony appears in the app grid (Linux) |
| `make blocks`         | build the five block images (`symphony/<block>:dev`) |
| `make test`           | Go unit tests + blocks contract test (blocks run as processes, no Docker) |
| `make integration`    | engine tests against the host's Docker daemon (real containers) |
| `make tag vX.Y.Z`     | tag HEAD and push the tag, which publishes a release (see below) |
| `make image`          | (re)build the `symphony-builder` image from `src/build/docker` |
| `make shell`          | interactive shell inside the builder container |
| `make clean`          | remove `bin/`, `src/build/bin/`, `src/client/dist/` |
| `make clean-docker`   | `clean` plus remove the builder image and cache volumes |

The output is a native binary for `PLATFORM` (default `linux/amd64`), linked
against glibc, GTK 3 and WebKitGTK 4.1 from Debian 12.
`make PLATFORM=windows/amd64` cross-compiles `bin/symphony.exe` from the same
container. macOS cannot be cross-compiled without Apple's SDK; build natively
on a Mac with `make build-local`.

After changing anything in `src/blocks`, run `make blocks`: the app only
builds an image itself when it is missing.

### Native build (optional)

Without Docker, install Go 1.27+, Node 22+ and the Wails platform
dependencies. On Debian/Ubuntu:

    sudo apt install gcc pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev

Then:

    make deps         # go mod tidy, npm install, install the wails CLI if absent
    make build-local  # wails build on the host -> ./bin/symphony
    make dev          # wails dev with hot reload (needs a display)
    make check        # go vet + client typecheck
    make bindings     # regenerate src/client/wailsjs after changing the Go API

On Linux the Makefile adds `-tags webkit2_41` when only WebKitGTK 4.1 is
installed. On macOS `./bin/symphony` is a launcher script next to the copied
`.app` bundle.

### Layout

    Makefile               build, test and release targets
    .github/workflows/     release.yml: build and publish on a vX.Y.Z tag
    src/                   Wails project root and Go module "symphony"
      main.go              desktop entry: embeds the frontend and the block sources
      cmd/symphony/        the same app serving client/dist from disk (go run)
      engine/              the backend: api (Wails bindings), core (deployments),
                           planner, runtime (Docker / fake), metrics, tests, ...
      blocks/              the five block images and their shared library
      client/              React + React Flow frontend; wailsjs/ is generated
      build/               builder image and platform assets

## Releasing

    make tag v1.2.3

checks that the version is `vX.Y.Z`, that the working tree has no uncommitted
changes, that the tag does not exist yet and that HEAD is already on the
remote, then creates an annotated tag and pushes it. The push starts
`.github/workflows/release.yml`, which:

1. builds the Linux and the Windows binary in the builder container;
2. runs the release gate (`.github/scripts/verify-release.sh`) on a clean
   Ubuntu runner: the Linux binary must be a plain ELF executable that the
   desktop recognises as `application/x-executable`, and must start headless
   with no terminal and no stdin and keep running while starting nothing but
   WebKit's own helper processes; the Windows binary must be a GUI-subsystem
   executable, so no console window opens with it;
3. only if the gate passes, publishes the GitHub Release with both binaries,
   `SHA256SUMS` and notes generated from the commits since the last tag.

Starting the workflow by hand from the Actions tab runs steps 1 and 2 only,
which is a dry run of a release.

## What is still simulated

| piece | today | where the real thing goes |
|-------|-------|---------------------------|
| Network-level shaping (bandwidth, netem) | done in the proxy block at HTTP level | tc/netem needs `NET_ADMIN`; add per-link if a scenario needs it on a link without a proxy |
| Third-party images | every node runs a Symphony block | a thin adapter block speaking the same contract in front of a real Postgres/Kafka |
| Metrics on the fake runtime | random walk derived from engine state | only used when Docker is unavailable |
