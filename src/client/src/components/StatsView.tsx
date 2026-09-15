import { useMemo } from 'react'
import { fmtBytes, fmtNum } from '../lib/format'
import { useSymphonyStore } from '../store/useSymphonyStore'
import { Sparkline } from './Sparkline'

/** Points across the width of each chart: three minutes at one per second. */
const CAP = 180
const PERCENT: [number, number] = [0, 100]

/** Host machine usage: CPU, memory and network, one chart each, fed by the
 *  `host:stats` event every second. */
export function StatsView() {
  const samples = useSymphonyStore((s) => s.hostStats)

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
  const last = samples.length ? samples[samples.length - 1] : undefined
  const pct = (v: number) => `${fmtNum(v, 1)}%`
  const rate = (v: number) => `${fmtBytes(v)}/s`

  if (samples.length === 0) {
    return <div className="drawer-placeholder">waiting for the first host sample…</div>
  }

  return (
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
  )
}
