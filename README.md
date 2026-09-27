# Symphony

Desktop tool for benchmarking **orchestration vs. choreography** in microservice
systems: draw a topology as a graph, run it as real Docker containers, drive
load through it, inject faults, and watch how each coordination style behaves
under stress. Predefined test scenarios make the two styles comparable run for
run. `symphony-cli` does all of it from a terminal, on the same state.

## Download

Every `vX.Y.Z` tag publishes a release with four files (plus `SHA256SUMS`):

| file | what |
|------|------|
| `symphony-vX.Y.Z-linux-amd64`           | the app, Linux x86-64 |
| `symphony-vX.Y.Z-windows-amd64.exe`     | the app, Windows 10/11 x86-64 |
| `symphony-cli-vX.Y.Z-linux-amd64`       | the command line, Linux x86-64 |
| `symphony-cli-vX.Y.Z-windows-amd64.exe` | the command line, Windows 10/11 x86-64 |

Put the app and the CLI in the same directory: they keep their state in one
file next to the binary (see [State](#state)), so that is how they share it.

**Linux.** The binary needs glibc 2.34+ (Ubuntu 22.04, Debian 12, Fedora 35 or
newer) with GTK 3 and WebKitGTK 4.1:

    sudo apt install libgtk-3-0 libwebkit2gtk-4.1-0   # Debian / Ubuntu
    chmod +x symphony-vX.Y.Z-linux-amd64
    ./symphony-vX.Y.Z-linux-amd64                    # or double-click it

It is a GUI program and never opens a terminal. Keep a name like `symphony`
if you rename it: a file called `install` is taken for an INSTALL text file by
the desktop, which then opens it as a script. `symphony-cli` is a static
binary and needs nothing but Docker.

**Windows.** Run the `.exe`; nothing to install. It uses the WebView2 runtime
that ships with Windows 10/11 (the app offers to fetch it if it is missing).

**Docker** (Docker Engine on Linux, Docker Desktop on Windows) is required:
every topology runs as Docker containers, there is no simulated mode. The
first Apply builds the five block images from a copy of their sources
embedded in the app, which takes a minute or two; later runs reuse them.
Without a reachable Docker daemon the app still opens, with a notice across
the top saying Docker is not available and why; topologies can be drawn,
validated and planned, but Apply, Teardown and test runs stay disabled.
Symphony keeps asking the daemon, and as soon as Docker is started the notice
goes away and the deployments it left running are picked up.

## Using it

The map fills the window and the panels float over it.

- **Toolbar**: the topology's name, whether Docker answers (`docker` or
  `no docker`), and **Validate**, **Plan**, **Apply**, **Teardown** for the
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

## Command line

`symphony-cli` is the same app without the window: the same engine and API,
the same state database, the same containers. Whatever it applies, changes
or tears down shows up in an open window within a few seconds, and it works
on whatever the window applied.

    symphony-cli status                                   # Docker, the database, every deployment
    symphony-cli templates
    symphony-cli apply "Orchestration (saga)"             # waits until it runs
    symphony-cli inspect "Orchestration (saga)" payment   # health checks, config, limits
    symphony-cli load "Orchestration (saga)" rps=40 concurrency=32
    symphony-cli top "Orchestration (saga)"               # live metrics, Ctrl-C ends
    symphony-cli fault "Orchestration (saga)" e-proxy-pay drop rate=0.3 --for 20s
    symphony-cli fault "Orchestration (saga)" inventory pause --for 10s
    symphony-cli config "Orchestration (saga)" orchestrator timeout_ms=1500
    symphony-cli resources "Orchestration (saga)" payment --cpus 0.25 --memory-mb 128
    symphony-cli test t04-crash "Choreography (saga)" duration_s=45
    symphony-cli runs
    symphony-cli teardown "Orchestration (saga)"

| group | commands |
|-------|----------|
| topologies  | `templates`, `show`, `save`, `delete`, `validate`, `plan`, `apply`, `teardown`, `restore` |
| deployments | `status`, `inspect`, `config`, `resources`, `fault`, `clear`, `load`, `control`, `logs`, `top`, `ps`, `log` |
| tests       | `scenarios`, `test`, `runs`, `run`, `compare`, `rm-run` |

`symphony-cli help` lists them and `symphony-cli help <command>` explains
one. A template argument is a built-in or saved template's name, a
running deployment's name, or a topology JSON file (`show` prints one to
start from; `-` reads stdin); `--name` applies it under another name.
`fault` takes a node or an edge id (the fault then hits that link only);
kill, pause and a whole-node partition are done by Docker, so with `--for`
the CLI stays until they end. `--json` prints results for scripts, `-v`
every engine log line, and `test` exits with 3 when the run does not pass.

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

Everything Symphony creates carries `symphony.owner=<instance id>` labels, so
it never touches other containers.

### State

The state of every deployment lives in a SQLite database, `symphony.db`, in
the directory of the binary: its topology (with every hot config and
resource change made since), admin token, network, phase and the load its
gateway was told to drive without an end. It is written as things change,
before an apply creates anything, so it always describes what should be
running.

Containers keep running when the window closes. When Symphony opens again
(and whenever Docker comes back) it reads the database and:

- attaches deployments whose containers are all still up, as they are;
- recreates, from the stored topology, a deployment whose containers are
  gone or stopped (Docker restarted, the machine rebooted) or whose apply
  was cut short, and starts its load again;
- finishes a teardown that was cut short;
- resumes containers a pause fault left paused (faults end with the session
  that injected them), and removes containers of this installation that no
  deployment owns.

`symphony-cli restore` does the same from a terminal; other CLI commands
attach to what runs without repairing anything.

| environment variable | effect |
|----------------------|--------|
| `SYMPHONY_DB=<file>`           | keep the state database there instead of next to the binary (`--db` for the CLI) |
| `SYMPHONY_TEARDOWN_ON_EXIT=1`  | tear every deployment down when the window closes |
| `DOCKER_HOST`                  | the Docker daemon to use, as for the docker CLI |

If the binary's directory is not writable, the database goes to the user
config directory instead (`~/.config/symphony` on Linux, `%AppData%\symphony`
on Windows). That directory also holds `templates.json` (saved templates) and
`runs/` (test results). A database created next to a binary of an older
version takes over the instance id and deployment records it kept there, so
its containers are still recognised.

## Build from source

The build runs inside a Docker container, so the host needs only **Docker**
and **make**:

    make                # -> ./bin/symphony and ./bin/symphony-cli
    ./bin/symphony      # or: make run

| target | does |
|--------|------|
| `make` / `make build` | `wails build` and the CLI build in the container, then copy both to `./bin` |
| `make cli`            | build only `./bin/symphony-cli` |
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

The app is a native binary for `PLATFORM` (default `linux/amd64`), linked
against glibc, GTK 3 and WebKitGTK 4.1 from Debian 12.
`make PLATFORM=windows/amd64` cross-compiles `bin/symphony.exe` and
`bin/symphony-cli.exe` from the same container. macOS cannot be
cross-compiled without Apple's SDK; build natively on a Mac with
`make build-local`.

The CLI is the same Go package built with a different tag: the module root
builds the window by default and `symphony-cli` with `-tags cli`. It uses no
webview and no cgo, so outside the container it is just

    cd src && CGO_ENABLED=0 go build -tags cli -o ../bin/symphony-cli .

After changing anything in `src/blocks`, run `make blocks`: the app only
builds an image itself when it is missing.

### Native build (optional)

Without Docker, install Go 1.27+, Node 22+ and the Wails platform
dependencies. On Debian/Ubuntu:

    sudo apt install gcc pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev

Then:

    make deps         # go mod tidy, npm install, install the wails CLI if absent
    make build-local  # wails build + CLI build on the host -> ./bin
    make dev          # wails dev with hot reload (needs a display)
    make check        # go vet (window and CLI builds) + client typecheck
    make bindings     # regenerate src/client/wailsjs after changing the Go API

On Linux the Makefile adds `-tags webkit2_41` when only WebKitGTK 4.1 is
installed. On macOS `./bin/symphony` is a launcher script next to the copied
`.app` bundle.

### Layout

    Makefile               build, test and release targets
    .github/workflows/     release.yml: build and publish on a vX.Y.Z tag
    src/                   Wails project root and Go module "symphony"
      main.go              the window's entry: embeds the frontend
      main_cli.go          symphony-cli's entry (-tags cli)
      blocks.go            the block sources both binaries embed
      cmd/symphony/        the window serving client/dist from disk (go run)
      engine/              the backend: backend (assembles the services),
                           api (what the window and the CLI call), app (the
                           Wails window), cli, core (deployments), store
                           (SQLite state), planner, runtime (Docker),
                           metrics, tests, ...
      blocks/              the five block images and their shared library
      client/              React + React Flow frontend; wailsjs/ is generated
      build/               builder image and platform assets

## Releasing

    make tag v1.2.3

checks that the version is `vX.Y.Z`, that the working tree has no uncommitted
changes, that the tag does not exist yet and that HEAD is already on the
remote, then creates an annotated tag and pushes it. The push starts
`.github/workflows/release.yml`, which:

1. builds the Linux and the Windows binaries (the app and the CLI) in the
   builder container;
2. runs the release gate (`.github/scripts/verify-release.sh`) on a clean
   Ubuntu runner: the Linux binary must be a plain ELF executable that the
   desktop recognises as `application/x-executable`, and must start headless
   with no terminal, no stdin and no Docker, and keep running while starting nothing but
   WebKit's own helper processes; the Windows binary must be a GUI-subsystem
   executable, so no console window opens with it;
3. runs a smoke test of the Linux CLI;
4. only if all of that passes, publishes the GitHub Release with the four
   binaries, `SHA256SUMS` and notes generated from the commits since the
   last tag.

Starting the workflow by hand from the Actions tab runs steps 1 to 3 only,
which is a dry run of a release.

## What is still simulated

| piece | today | where the real thing goes |
|-------|-------|---------------------------|
| Network-level shaping (bandwidth, netem) | done in the proxy block at HTTP level | tc/netem needs `NET_ADMIN`; add per-link if a scenario needs it on a link without a proxy |
| Third-party images | every node runs a Symphony block | a thin adapter block speaking the same contract in front of a real Postgres/Kafka |
