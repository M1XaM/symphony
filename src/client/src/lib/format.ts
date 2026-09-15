export function fmtTime(ts: number): string {
  const d = new Date(ts)
  const hh = String(d.getHours()).padStart(2, '0')
  const mm = String(d.getMinutes()).padStart(2, '0')
  const ss = String(d.getSeconds()).padStart(2, '0')
  const ms = String(d.getMilliseconds()).padStart(3, '0')
  return `${hh}:${mm}:${ss}.${ms}`
}

export function fmtNum(n: number | undefined, digits = 0): string {
  if (n === undefined || Number.isNaN(n)) return '—'
  return n.toFixed(digits)
}

export const KIND_LABEL: Record<string, string> = {
  service: 'Service',
  broker: 'Broker',
  db: 'Database',
  proxy: 'Proxy',
  infra: 'Gateway',
}

const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB']

/** 1536 -> "1.5 KB". Whole bytes stay integers. */
export function fmtBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return '—'
  let v = n
  let i = 0
  while (v >= 1024 && i < BYTE_UNITS.length - 1) {
    v /= 1024
    i++
  }
  return `${i === 0 ? Math.round(v) : v.toFixed(1)} ${BYTE_UNITS[i]}`
}
