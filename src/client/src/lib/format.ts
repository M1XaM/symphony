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
