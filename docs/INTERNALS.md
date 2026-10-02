# Symphony internals

How Symphony runs a topology, where it keeps its state, how it is built and
how it is released. Using it is in the [README](../README.md); the command
line is in [CLI.md](CLI.md).

- [How it runs](#how-it-runs)
- [State](#state)
- [Build from source](#build-from-source)
- [Releasing](#releasing)

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
Engine API directly. The contract is documented in [`src/blocks/README.md`](../src/blocks/README.md).

Everything Symphony creates carries `symphony.owner=<instance id>` labels, so
it never touches other containers.

## State

The state of every deployment lives in a SQLite database, `symphony.db`, in
the directory of the binary: its topology (with every hot config and
resource change made since), admin token, network, phase, the faults
injected into it and the load its gateway was told to drive without an end.
It is written as things change, before an apply creates anything, so it
always describes what should be running.

Containers keep running when the window closes. When Symphony opens again
(and whenever Docker comes back) it reads the database and:

- attaches deployments whose containers are all still up, as they are;
- recreates, from the stored topology, a deployment whose containers are
  gone or stopped (Docker restarted, the machine rebooted) or whose apply
  was cut short, and starts its load again;
- finishes a teardown that was cut short;
- ends the faults that became due while it was closed (a paused container
  is resumed, a killed one started, a partitioned one reconnected) and
  keeps the timers of the ones still running: faults are stored with the
  deployment, so they do not depend on the process that injected them;
- removes containers of this installation that no deployment owns.

`symphony-cli restore` does the same from a terminal; other CLI commands
attach to what runs without repairing anything.

| environment variable | effect |
|----------------------|--------|
| `SYMPHONY_DB=<file>`           | keep the state database there instead of next to the binary (`--db` for the CLI) |
| `SYMPHONY_TEARDOWN_ON_EXIT=1`  | tear every deployment down when the window closes |
| `DOCKER_HOST`                  | the Docker daemon to use, as for the docker CLI |

If the binary's directory is not writable, the database goes to the user
config directory instead (`~/.config/symphony` on Linux, `%AppData%\symphony`
on Windows). That directory also holds `templates.json` (saved templates).
Test results are in the database too, so the window and the CLI see the same
runs. A database created next to a binary of an older version takes over
what that version kept in the config directory: the instance id and the
deployment records, so its containers are still recognised, and the runs it
stored as files in `runs/`.

## Build from source

The build runs inside a Docker container, so the host needs only **Docker**
and **make**:

    make                    # -> bin/linux/ and bin/windows/
    ./bin/linux/symphony    # or: make run

    bin/
      linux/     symphony  symphony-cli  symphony.desktop
      windows/   symphony.exe  symphony-cli.exe

| target | does |
|--------|------|
| `make` / `make build` | `wails build` and the CLI build in the container, for Linux and Windows, into `bin/<os>/` |
| `make cli`            | build only the `symphony-cli` binaries |
| `make dist VERSION=vX.Y.Z` | zip `bin/` into `dist/`: the two release archives and `SHA256SUMS` |
| `make run`            | build for Linux, then launch `bin/linux/symphony` |
| `make desktop`        | register `bin/linux/symphony.desktop` with the desktop, so Symphony appears in the app grid (Linux) |
| `make blocks`         | build the five block images (`symphony/<block>:dev`) |
| `make test`           | Go unit tests + blocks contract test (blocks run as processes, no Docker), and the client's unit tests |
| `make lint`           | gofmt, `go vet` (window and CLI builds) and the client typecheck, in the container |
| `make integration`    | engine tests against the host's Docker daemon (real containers) |
| `make docs`           | regenerate `docs/CLI.md` from the CLI's command table (`make lint` fails when it is out of date) |
| `make tag vX.Y.Z`     | tag HEAD and push the tag, which publishes a release (see [Releasing](#releasing)) |
| `make image`          | (re)build the `symphony-builder` image from `src/build/docker` |
| `make shell`          | interactive shell inside the builder container |
| `make clean`          | remove `bin/`, `dist/`, `src/build/bin/`, `src/client/dist/` |
| `make clean-docker`   | `clean` plus remove the builder image and cache volumes |

The Linux app is linked against glibc, GTK 3 and WebKitGTK 4.1 from
Debian 12; the Windows one is cross-compiled in the same container.
`PLATFORM=linux/amd64` (or `windows/amd64`) builds one platform only. macOS
cannot be cross-compiled without Apple's SDK; build natively on a Mac with
`make build-local` (output in `bin/darwin/`).

The CLI is the same Go package built with a different tag: the module root
builds the window by default and `symphony-cli` with `-tags cli`. It uses no
webview and no cgo, so outside the container it is just

    cd src && CGO_ENABLED=0 go build -tags cli -o ../bin/linux/symphony-cli .

After changing anything in `src/blocks`, run `make blocks`: an image built
that way is the developer's, and the app leaves it alone unless it was built
for another version of the blocks contract (`wire.Contract`, the
`symphony.contract` image label). The images the app builds itself carry a
digest of the sources they came from (`symphony.source`), so a newer
Symphony replaces the ones an older one built.

### Native build (optional)

Without Docker, install Go 1.27+, Node 22+ and the Wails platform
dependencies. On Debian/Ubuntu:

    sudo apt install gcc pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev

Then:

    make deps         # go mod tidy, npm install, install the wails CLI if absent
    make build-local  # wails build + CLI build on the host -> bin/<host os>/
    make dev          # wails dev with hot reload (needs a display)
    make check        # go vet (window and CLI builds) + client typecheck
    make bindings     # regenerate src/client/wailsjs after changing the Go API

On Linux the Makefile adds `-tags webkit2_41` when only WebKitGTK 4.1 is
installed. On macOS `bin/darwin/symphony` is a launcher script next to the copied
`.app` bundle.

### Layout

    Makefile               build, test and release targets
    docs/                  CLI.md (generated: the CLI's guide) and this file
    .github/workflows/     ci.yml: lint and tests on every push and pull request
                           release.yml: build and publish on a vX.Y.Z tag
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

1. runs the lint and the unit tests, then builds the Linux and the Windows
   binaries (the app and the CLI) in the builder container;
2. runs the release gate (`.github/scripts/verify-release.sh`) on a clean
   Ubuntu runner: the Linux binary must be a plain ELF executable that the
   desktop recognises as `application/x-executable`, and must start headless
   with no terminal, no stdin and no Docker, and keep running while starting nothing but
   WebKit's own helper processes; the Windows binary must be a GUI-subsystem
   executable, so no console window opens with it;
3. runs a smoke test of the Linux CLI;
4. zips each platform (`make dist`), each with the `GUIDE.md` the CLI
   generates;
5. only if all of that passes, publishes the GitHub Release with the two
   zips, `SHA256SUMS` and notes generated from the commits since the last
   tag.

Starting the workflow by hand from the Actions tab runs steps 1 to 4 only,
which is a dry run of a release (the zips are kept as a workflow artifact).
