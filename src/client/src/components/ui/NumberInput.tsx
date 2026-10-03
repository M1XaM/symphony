interface Props {
  value: number | ''
  /** Called with what was typed (NaN for an empty field) or stepped to. */
  onValue(n: number): void
  min?: number
  max?: number
  step?: number
  placeholder?: string
  className?: string
}

/** How many decimals a step has, so stepping 0.1 three times gives 0.3. */
function decimals(step: number): number {
  const s = String(step)
  const dot = s.indexOf('.')
  return dot < 0 ? 0 : s.length - dot - 1
}

/** A number field with the app's own up and down buttons in place of the
 *  platform's spinner. */
export function NumberInput({ value, onValue, min, max, step = 1, placeholder, className }: Props) {
  const bump = (dir: 1 | -1) => {
    const from = value === '' ? (min ?? 0) - (dir > 0 ? step : 0) : value
    let next = Number((from + dir * step).toFixed(decimals(step)))
    if (min !== undefined) next = Math.max(min, next)
    if (max !== undefined) next = Math.min(max, next)
    onValue(next)
  }
  return (
    <span className={`num${className ? ` ${className}` : ''}`}>
      <input
        type="number"
        className="input num-field"
        min={min}
        max={max}
        step={step}
        placeholder={placeholder}
        value={value}
        onChange={(e) => onValue(e.target.value === '' ? NaN : Number(e.target.value))}
      />
      <span className="num-steps">
        <button type="button" className="num-step" tabIndex={-1} aria-label="increase" onClick={() => bump(1)}>
          <span className="num-up" aria-hidden="true" />
        </button>
        <button type="button" className="num-step" tabIndex={-1} aria-label="decrease" onClick={() => bump(-1)}>
          <span className="num-down" aria-hidden="true" />
        </button>
      </span>
    </span>
  )
}
