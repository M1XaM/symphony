import type { FaultSpec } from '../../types/topology'

export interface FaultButton {
  label: string
  title: string
  fault: FaultSpec
}

/** Faults that hit the whole node. Container-level ones go through Docker,
 *  the rest through the block's /control. */
export const NODE_FAULTS: { group: string; items: FaultButton[] }[] = [
  {
    group: 'container',
    items: [
      { label: 'kill 5s', title: 'SIGKILL the container, start it again after 5 s', fault: { kind: 'kill', duration_ms: 5000, params: {} } },
      { label: 'pause 5s', title: 'freeze the process for 5 s', fault: { kind: 'pause', duration_ms: 5000, params: {} } },
      { label: 'partition 10s', title: 'disconnect the container from the network for 10 s', fault: { kind: 'partition', duration_ms: 10000, params: {} } },
    ],
  },
  {
    group: 'inside',
    items: [
      { label: 'burn 1 cpu 10s', title: 'busy-loop one core inside the container', fault: { kind: 'cpu_stress', duration_ms: 10000, params: { cores: 1 } } },
      { label: '+200 MB 10s', title: 'allocate and touch 200 MB inside the container', fault: { kind: 'memory_pressure', duration_ms: 10000, params: { mb: 200 } } },
      { label: 'stall 5s', title: 'freeze the data path; the admin port keeps answering', fault: { kind: 'stall', duration_ms: 5000, params: {} } },
      { label: 'crash', title: 'the process exits; clear faults starts it again', fault: { kind: 'crash', duration_ms: 0, params: { exit_code: 137 } } },
    ],
  },
  {
    group: 'all links',
    items: [
      { label: '+250 ms 10s', title: 'delay every request in and out', fault: { kind: 'latency', duration_ms: 10000, params: { ms: 250, jitter_ms: 50 } } },
      { label: 'drop 30% 10s', title: 'swallow 30 % of requests', fault: { kind: 'drop', duration_ms: 10000, params: { rate: 0.3 } } },
      { label: 'error 30% 10s', title: 'answer 30 % of requests with 503', fault: { kind: 'error', duration_ms: 10000, params: { rate: 0.3, status: 503 } } },
      { label: 'timeout 30% 10s', title: 'hang on 30 % of requests until the caller gives up', fault: { kind: 'timeout', duration_ms: 10000, params: { rate: 0.3 } } },
    ],
  },
]

/** Faults scoped to one edge, injected on the sender. */
export const EDGE_FAULTS: FaultButton[] = [
  { label: '+300 ms', title: 'delay on this link for 10 s', fault: { kind: 'latency', duration_ms: 10000, params: { ms: 300, jitter_ms: 50 } } },
  { label: 'drop 50%', title: 'drop half the requests on this link for 10 s', fault: { kind: 'drop', duration_ms: 10000, params: { rate: 0.5 } } },
  { label: 'drop 100%', title: 'cut the link for 10 s', fault: { kind: 'drop', duration_ms: 10000, params: { rate: 1 } } },
  { label: 'error 50%', title: '503 on half the requests for 10 s', fault: { kind: 'error', duration_ms: 10000, params: { rate: 0.5 } } },
  { label: 'timeout 30%', title: 'hang 30 % of requests for 10 s', fault: { kind: 'timeout', duration_ms: 10000, params: { rate: 0.3 } } },
  { label: 'duplicate 30%', title: 'deliver 30 % of requests twice for 10 s', fault: { kind: 'duplicate', duration_ms: 10000, params: { rate: 0.3 } } },
  { label: 'slow 2 KB/s', title: 'throttle responses for 10 s', fault: { kind: 'slow', duration_ms: 10000, params: { bytes_per_sec: 2048 } } },
]
