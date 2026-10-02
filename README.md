# Symphony

Desktop tool for benchmarking **orchestration vs. choreography** in microservice
systems: draw a topology as a graph, run it as real Docker containers, drive
load through it, inject faults, and watch how each coordination style behaves
under stress. Predefined test scenarios make the two styles comparable run for
run. `symphony-cli` does all of it from a terminal, on the same state.

## Download

Every `vX.Y.Z` tag publishes a release with one zip per platform (plus
`SHA256SUMS`):

| archive | contains |
|---------|----------|
| `symphony-vX.Y.Z-linux-amd64.zip`   | `symphony` (the app), `symphony-cli` (the command line) and a short `README.md`, Linux x86-64 |
| `symphony-vX.Y.Z-windows-amd64.zip` | `symphony.exe`, `symphony-cli.exe` and a short `README.md`, Windows 10/11 x86-64 |

Each unzips to a folder of the same name. Keep the app and the CLI together
in it: they keep their state in one file next to the binary (see
[State](docs/INTERNALS.md#state)), so that is how they share it.

**Linux.** The binary needs glibc 2.34+ (Ubuntu 22.04, Debian 12, Fedora 35 or
newer) with GTK 3 and WebKitGTK 4.1:

    sudo apt install libgtk-3-0 libwebkit2gtk-4.1-0   # Debian / Ubuntu
    unzip symphony-vX.Y.Z-linux-amd64.zip
    ./symphony-vX.Y.Z-linux-amd64/symphony           # or double-click it

It is a GUI program and never opens a terminal. Keep a name like `symphony`
if you rename it: a file called `install` is taken for an INSTALL text file by
the desktop, which then opens it as a script. `symphony-cli` is a static
binary and needs nothing but Docker.

**Windows.** Unzip and run `symphony.exe`; nothing to install. It uses the WebView2 runtime
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

Its guide is **[docs/CLI.md](docs/CLI.md)**: the concepts and conventions,
the usual workflows with examples, every command, the fault kinds and the
scenarios, and a section of rules for agentic models that drive it. The
binary writes the same guide on request, and answers for itself:

    symphony-cli --help              # every command
    symphony-cli <command> --help    # one command, with examples
    symphony-cli generate-guide      # writes GUIDE.md next to the binary
    symphony-cli --json-help         # the whole catalogue as JSON

## Internals

**[docs/INTERNALS.md](docs/INTERNALS.md)** covers how a topology becomes
containers (the five block images, their wiring and the admin port), where
the state lives and what happens to it across restarts, building from source
(the host needs only Docker and make), and how a release is made.

## What is real and what is simulated

Real: every node is a container on a Docker network of its own, with real
HTTP between them, real cgroup limits (CPU throttling, the OOM killer), and
kill, pause and network partition done by Docker.

| piece | today | where the real thing goes |
|-------|-------|---------------------------|
| What a service does | a behaviour program: burn CPU, wait, call, publish, use the database. There is no business logic | an image of your own that speaks the blocks contract ([`src/blocks/README.md`](src/blocks/README.md)) |
| Broker | the `broker` block: topics, partitions, consumer groups and at-least-once push delivery, in memory. Its log is gone when the container restarts | an adapter block that speaks the contract on the admin port in front of a real Kafka or RabbitMQ |
| Database | the `database` block: an in-memory key-value store with a connection pool, a latency model, row locks and a storage cap. Its rows are gone when the container restarts | the same kind of adapter in front of a real Postgres |
| Link faults (latency, drop, error, timeout, slow, corrupt, duplicate) | applied by the blocks themselves to HTTP requests, on either end of any edge; the proxy block is one more place to put them. Packets are never touched | `tc`/netem in the container's network namespace, which needs `NET_ADMIN` |
| Load | synthetic sagas minted by the gateway block's generator | publishing the gateway's ingress port, so any load tool can drive a topology |
| Third-party images | a node's image can be changed, but only Symphony's blocks have the admin port and the per-edge endpoints: another image can be started, limited, killed and paused, and nothing else | the adapter blocks above |
| Scale | one Docker host | another implementation of the engine's `Runtime` interface (Kubernetes) |
