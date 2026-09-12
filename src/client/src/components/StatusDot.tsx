import type { NodeState } from '../types/topology'

export function StatusDot({ state, title }: { state?: NodeState; title?: string }) {
  const s = state ?? 'unknown'
  return <span className={`dot dot-${s}`} title={title ?? s} aria-label={s} />
}
