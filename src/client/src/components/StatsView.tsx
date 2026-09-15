import { useMemo } from 'react'
import { fmtBytes, fmtNum } from '../lib/format'
import { selectContainers, selectIsApplied, selectSimHistory, useSymphonyStore } from '../store/useSymphonyStore'
import { Sparkline } from './Sparkline'

/** Points across the width of each chart: three minutes at one per second. */
const CAP = 180
const PERCENT: [number, number] = [0, 100]

/** Host machine usage next to the running simulation's container totals. */
export function StatsView() {
  const samples = useSymphonyStore((s) => s.hostStats)
  const sim = useSymphonyStore(selectSimHistory)
  const containers = useSymphonyStore(selectContainers)
  const applied = useSymphonyStore(selectIsApplied)
  const name = useSymphonyStore((s) => s.topology.name)

  const { cpu, mem, rx, tx } = useMemo(
    () => ({
      cpu: [{ name: 'cpu', data: samples.map((s) => s.cpu_pct), className: 'series-1' }],
      mem: [{ name: 'ram', data: samples.map((s) => s.mem_pct), className: 'series-1' }],
      rx: { name: 'in', data: samples.map((s) => s.net_rx_bps), className: 'series-1' },
      tx: { name: 'out', data: samples.map((s) => s.net_tx_bps), className: 'series-2' },
    }),
    [samples],
  )
  const net = useMemo(() => [rx, tx], [rx, tx])
  const simCpu = useMemo(() => [{ name: 'cpu', data: sim.map((s) => s.cpu_pct), className: 'series-2' }], [sim])
  const simMem = useMemo(() => [{ name: 'ram', data: sim.map((s) => s.mem_mb), className: 'series-2' }], [sim])
  const last = samples.length ? samples[samples.length - 1] : undefined
  const pct = (v: number) => `${fmtNum(v, 1)}%`
  const rate = (v: number) => `${fmtBytes(v)}/s`
  const nodes = Object.values(containers).sort((a, b) => a.node_id.localeCompare(b.node_id))

  return (
    <div className="stats-wrap">
      <div className="stats-head muted small">host machine</div>
      {samples.length === 0 ? (
        <div className="drawer-placeholder">waiting for the first host sample…</div>
      ) : (
        <div className="stats">
          <Sparkline title="CPU" series={cpu} cap={CAP} format={pct} domain={PERCENT} footer="all cores · 1 s" />
          <Sparkline
            title="RAM"
            series={mem}
            cap={CAP}
            format={pct}
            domain={PERCENT}
            footer={last ? `${fmtBytes(last.mem_used_bytes)} of ${fmtBytes(last.mem_total_bytes)} · 1 s` : undefined}
          />
          <Sparkline title="Network" series={net} cap={CAP} format={rate} footer="all interfaces · 1 s" />
        </div>
      )}
      {applied ? (
        <>
          <div className="stats-head muted small">simulation · {name}</div>
          <div className="stats stats-sim">
            <Sparkline title="Containers CPU" series={simCpu} cap={CAP} format={(v) => `${fmtNum(v, 0)}%`} footer="sum over the deployment · 100% = one core" />
            <Sparkline title="Containers RAM" series={simMem} cap={CAP} format={(v) => `${fmtNum(v, 0)} MB`} footer="sum over the deployment" />
            <div className="chart">
              <div className="chart-head">
                <span className="pane-title">Per node</span>
              </div>
              <table className="plan-table">
                <thead>
                  <tr>
                    <th>node</th>
                    <th>cpu</th>
                    <th>ram</th>
                    <th>net</th>
                  </tr>
                </thead>
                <tbody>
                  {nodes.map((c) => (
                    <tr key={c.node_id}>
                      <td className="mono">{c.node_id}</td>
                      <td className="mono">{fmtNum(c.cpu_pct, 1)}%</td>
                      <td className="mono">
                        {fmtBytes(c.mem_used_bytes)}
                        {c.mem_limit_bytes ? <span className="muted"> / {fmtBytes(c.mem_limit_bytes)}</span> : null}
                      </td>
                      <td className="mono">
                        ↓{fmtBytes(c.net_rx_bps)}/s ↑{fmtBytes(c.net_tx_bps)}/s
                      </td>
                    </tr>
                  ))}
                  {nodes.length === 0 ? (
                    <tr>
                      <td colSpan={4} className="muted">
                        no container samples yet
                      </td>
                    </tr>
                  ) : null}
                </tbody>
              </table>
            </div>
          </div>
        </>
      ) : null}
    </div>
  )
}
