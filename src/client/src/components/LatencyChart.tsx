import { useMemo, useState, type MouseEvent } from 'react'
import { fmtNum } from '../lib/format'
import { useSymphonyStore } from '../store/useSymphonyStore'

const W = 300
const H = 90
const PAD_Y = 6
const CAP = 120

/**
 * Hand-rolled single-series line: no chart library. One accent colour, 2px
 * line, three recessive gridlines, hover crosshair with a value readout.
 * Fed by the `metrics:tick` event every 500 ms.
 */
export function LatencyChart() {
  const data = useSymphonyStore((s) => s.latency)
  const [hover, setHover] = useState<number | null>(null)

  const { path, area, min, max, xs, ys, gridYs } = useMemo(() => {
    if (data.length === 0) {
      return { path: '', area: '', min: 0, max: 0, xs: [] as number[], ys: [] as number[], gridYs: [] as number[] }
    }
    const lo = Math.min(...data)
    const hi = Math.max(...data)
    const span = Math.max(hi - lo, 10)
    const yMin = Math.max(0, lo - span * 0.1)
    const yMax = hi + span * 0.1
    const scaleY = (v: number) => H - PAD_Y - ((v - yMin) / (yMax - yMin)) * (H - PAD_Y * 2)
    const scaleX = (i: number) => (i / (CAP - 1)) * W
    // Right-align the series so it scrolls in from the right as points arrive.
    const offset = CAP - data.length
    const xs = data.map((_, i) => scaleX(i + offset))
    const ys = data.map(scaleY)
    const d = xs.map((x, i) => `${i === 0 ? 'M' : 'L'}${x.toFixed(1)},${ys[i].toFixed(1)}`).join(' ')
    const area = `${d} L${xs[xs.length - 1].toFixed(1)},${H} L${xs[0].toFixed(1)},${H} Z`
    const gridYs = [0.25, 0.5, 0.75].map((f) => PAD_Y + f * (H - PAD_Y * 2))
    return { path: d, area, min: lo, max: hi, xs, ys, gridYs }
  }, [data])

  const onMove = (e: MouseEvent<SVGSVGElement>) => {
    if (xs.length === 0) return
    const rect = e.currentTarget.getBoundingClientRect()
    const x = ((e.clientX - rect.left) / rect.width) * W
    let best = 0
    let bestDist = Infinity
    for (let i = 0; i < xs.length; i++) {
      const d = Math.abs(xs[i] - x)
      if (d < bestDist) {
        bestDist = d
        best = i
      }
    }
    setHover(best)
  }

  const last = data.length ? data[data.length - 1] : undefined
  const shown = hover !== null && hover < data.length ? data[hover] : last

  return (
    <div className="chart">
      <div className="chart-head">
        <span className="pane-title">Latency</span>
        <span className="chart-readout">
          <span className="chart-value mono">{fmtNum(shown, 1)}</span>
          <span className="muted"> ms{hover !== null ? ' · hover' : ' · live'}</span>
        </span>
      </div>
      <svg
        className="chart-svg"
        viewBox={`0 0 ${W} ${H}`}
        preserveAspectRatio="none"
        onMouseMove={onMove}
        onMouseLeave={() => setHover(null)}
        role="img"
        aria-label="latency over the last minute"
      >
        {gridYs.map((y) => (
          <line key={y} x1={0} x2={W} y1={y} y2={y} className="chart-grid" vectorEffect="non-scaling-stroke" />
        ))}
        {area ? <path d={area} className="chart-area" /> : null}
        {path ? <path d={path} className="chart-line" vectorEffect="non-scaling-stroke" /> : null}
        {hover !== null && hover < xs.length ? (
          <>
            <line
              x1={xs[hover]}
              x2={xs[hover]}
              y1={0}
              y2={H}
              className="chart-crosshair"
              vectorEffect="non-scaling-stroke"
            />
            <circle cx={xs[hover]} cy={ys[hover]} r={3} className="chart-marker" vectorEffect="non-scaling-stroke" />
          </>
        ) : null}
      </svg>
      <div className="chart-foot muted">
        <span>min {fmtNum(min, 0)}</span>
        <span>{data.length} pts · 500 ms</span>
        <span>max {fmtNum(max, 0)}</span>
      </div>
    </div>
  )
}
