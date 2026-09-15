import { useMemo, useState, type MouseEvent } from 'react'

const W = 300
const H = 90
const PAD_Y = 6

export interface Series {
  name: string
  data: number[]
  /** class applied to the line, area and marker: sets the series colour */
  className: string
}

interface Props {
  title: string
  series: Series[]
  /** points that fill the width; shorter series scroll in from the right */
  cap: number
  format(v: number): string
  /** fixed y range; omitted means fit to the data */
  domain?: [number, number]
  /** shown under the plot, e.g. the sample interval */
  footer?: string
}

/**
 * Hand-rolled line chart: no chart library. Thin lines, three recessive
 * gridlines, hover crosshair with a readout of every series at that point.
 * With two or more series a legend sits in the header; one series needs none.
 */
export function Sparkline({ title, series, cap, format, domain, footer }: Props) {
  const [hover, setHover] = useState<number | null>(null)
  const len = Math.max(0, ...series.map((s) => s.data.length))

  const { paths, xs, gridYs } = useMemo(() => {
    const all = series.flatMap((s) => s.data)
    if (all.length === 0) {
      return { paths: [] as { line: string; area: string; ys: number[] }[], xs: [] as number[], gridYs: [] as number[] }
    }
    let yMin: number
    let yMax: number
    if (domain) {
      ;[yMin, yMax] = domain
    } else {
      const lo = Math.min(...all)
      const hi = Math.max(...all)
      const span = Math.max(hi - lo, Math.abs(hi) * 0.1, 1)
      yMin = Math.max(0, lo - span * 0.1)
      yMax = hi + span * 0.1
    }
    const scaleY = (v: number) => H - PAD_Y - ((Math.min(yMax, Math.max(yMin, v)) - yMin) / (yMax - yMin)) * (H - PAD_Y * 2)
    const scaleX = (i: number) => (i / (cap - 1)) * W
    // Right-align the series so it scrolls in from the right as points arrive.
    const offset = cap - len
    const xs = Array.from({ length: len }, (_, i) => scaleX(i + offset))
    const paths = series.map((s) => {
      const start = len - s.data.length
      const ys = s.data.map(scaleY)
      const d = ys.map((y, i) => `${i === 0 ? 'M' : 'L'}${xs[start + i].toFixed(1)},${y.toFixed(1)}`).join(' ')
      const area = ys.length ? `${d} L${xs[len - 1].toFixed(1)},${H} L${xs[start].toFixed(1)},${H} Z` : ''
      return { line: d, area, ys }
    })
    const gridYs = [0.25, 0.5, 0.75].map((f) => PAD_Y + f * (H - PAD_Y * 2))
    return { paths, xs, gridYs }
  }, [series, cap, domain, len])

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

  const at = (s: Series, i: number | null): number | undefined => {
    const idx = i === null ? s.data.length - 1 : i - (len - s.data.length)
    return idx >= 0 && idx < s.data.length ? s.data[idx] : undefined
  }
  const hovering = hover !== null && hover < len

  return (
    <div className="chart">
      <div className="chart-head">
        <span className="pane-title">{title}</span>
        <span className="chart-readout">
          {series.map((s) => {
            const v = at(s, hovering ? hover : null)
            return (
              <span key={s.name} className="chart-series">
                {series.length > 1 ? <span className={`chart-swatch ${s.className}`} aria-hidden="true" /> : null}
                {series.length > 1 ? <span className="muted">{s.name} </span> : null}
                <span className="chart-value mono">{v === undefined ? '—' : format(v)}</span>
              </span>
            )
          })}
          <span className="muted small">{hovering ? ' · hover' : ' · live'}</span>
        </span>
      </div>
      <svg
        className="chart-svg"
        viewBox={`0 0 ${W} ${H}`}
        preserveAspectRatio="none"
        onMouseMove={onMove}
        onMouseLeave={() => setHover(null)}
        role="img"
        aria-label={title}
      >
        {gridYs.map((y) => (
          <line key={y} x1={0} x2={W} y1={y} y2={y} className="chart-grid" vectorEffect="non-scaling-stroke" />
        ))}
        {paths.map((p, i) =>
          p.area ? <path key={`a${i}`} d={p.area} className={`chart-area ${series[i].className}`} /> : null,
        )}
        {paths.map((p, i) =>
          p.line ? (
            <path key={`l${i}`} d={p.line} className={`chart-line ${series[i].className}`} vectorEffect="non-scaling-stroke" />
          ) : null,
        )}
        {hovering ? (
          <>
            <line x1={xs[hover]} x2={xs[hover]} y1={0} y2={H} className="chart-crosshair" vectorEffect="non-scaling-stroke" />
            {paths.map((p, i) => {
              const idx = hover - (len - series[i].data.length)
              return idx >= 0 && idx < p.ys.length ? (
                <circle
                  key={`m${i}`}
                  cx={xs[hover]}
                  cy={p.ys[idx]}
                  r={3}
                  className={`chart-marker ${series[i].className}`}
                  vectorEffect="non-scaling-stroke"
                />
              ) : null
            })}
          </>
        ) : null}
      </svg>
      {footer ? <div className="chart-foot muted">{footer}</div> : null}
    </div>
  )
}
