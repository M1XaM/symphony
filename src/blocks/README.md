# Symphony blocks

Universal container images, one per node kind, that Symphony wires into any
topology drawn on the canvas. A block does not know what it is until the
engine tells it through the environment; the same five images build every
system.

| block | kind | role |
|-------|------|------|
| `gateway`  | `infra`   | ingress + load generator; measures every saga end to end |
| `service`  | `service` | worker pool running a behaviour program per inbound edge |
| `broker`   | `broker`  | topics, partitions, consumer groups, at-least-once push delivery |
| `database` | `db`      | bounded connection pool, latency model, transactions, storage cap |
| `proxy`    | `proxy`   | forwards paired edges; where link faults are injected |

## Contract

Every block:

- serves the **data port 8080** with one `POST /in/<edge-id>` per inbound
  edge (from `SYMPHONY_LINKS`), and the **admin port 9090** with `GET /health`,
  `POST /control`, `GET /metrics`, `GET /logs`, `GET /events`, `GET /snapshot`
  (bearer token from `SYMPHONY_ADMIN_TOKEN`);
- reads `SYMPHONY_NODE`, `SYMPHONY_KIND`, `SYMPHONY_DEPLOYMENT`,
  `SYMPHONY_REPLICA`/`SYMPHONY_REPLICAS`, `SYMPHONY_LINKS` (JSON),
  `SYMPHONY_CONFIG` (JSON, hot+cold config), `SYMPHONY_BEHAVIOUR` (service);
- speaks the envelope in `common/wire`: JSON `{saga, step, payload, trace}`
  with `X-Symphony-Saga/Edge/Hop/Deadline/Attempt` headers;
- answers `/health` with a document of named checks (`healthy | degraded |
  failing`); `?strict=1` turns a non-healthy status into 503 for the container
  HEALTHCHECK;
- accepts `/control` commands `{id, op, target, params, duration_ms}` with the
  common ops `config.get/set`, `fault.set/clear/list`, `chaos.cpu/memory/
  stall/resume/crash`, `state.reset`, `log.level`, `links.get`, `targets.get`,
  `ops`, plus its own (see each `main.go`).

Fault kinds (`fault.set params.kind`): `drop`, `error`, `latency`, `timeout`,
`slow`, `partition`, `corrupt`, `duplicate`, `reorder`, `reject`. Targets:
`{edge, direction: in|out, peer, topic, table}`; empty = the whole block.

## Build

    make blocks                      # all five, tagged symphony/<block>:dev
    docker build -f blocks/Dockerfile --build-arg BLOCK=service -t symphony/service:dev src

The engine builds them itself from an embedded copy of this directory when
an image is missing, so a fresh install needs only Docker.

## Run one by hand

    SYMPHONY_NODE=payment SYMPHONY_LINKS='{"in":[{"edge":"e1","kind":"calls","from":"x","to":"payment"}]}' go run ./blocks/service
    curl -s -XPOST localhost:8080/in/e1 -d '{"saga":"s1","step":"reserve"}'
    curl -s localhost:9090/health | jq .status
    curl -s -XPOST localhost:9090/control -d '{"op":"fault.set","target":{"edge":"e1"},"params":{"kind":"drop","rate":0.5},"duration_ms":5000}'
