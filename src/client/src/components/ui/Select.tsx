import type { KeyboardEvent } from 'react'
import { createPortal } from 'react-dom'
import { usePopup } from './usePopup'

export interface SelectOption {
  value: string
  label: string
}

interface Props {
  value: string
  options: (SelectOption | string)[]
  onChange(value: string): void
  className?: string
  disabled?: boolean
  'aria-label'?: string
  title?: string
}

/** A dropdown drawn by the app rather than by the platform: the closed field
 *  looks like every other input, and the list is one of the app's own menus. */
export function Select({ value, options, onChange, className, disabled, title, 'aria-label': ariaLabel }: Props) {
  const { open, setOpen, anchorRef, popupRef, style } = usePopup<HTMLButtonElement>('start')
  const opts = options.map((o) => (typeof o === 'string' ? { value: o, label: o } : o))
  const index = opts.findIndex((o) => o.value === value)

  // the arrows step through the options whether the list is open or not
  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return
    e.preventDefault()
    const next = opts[Math.min(opts.length - 1, Math.max(0, index + (e.key === 'ArrowDown' ? 1 : -1)))]
    if (next && next.value !== value) onChange(next.value)
  }

  return (
    <>
      <button
        ref={anchorRef}
        type="button"
        className={`select${open ? ' is-open' : ''}${className ? ` ${className}` : ''}`}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label={ariaLabel}
        title={open ? undefined : title}
        disabled={disabled}
        onClick={() => setOpen(!open)}
        onKeyDown={onKeyDown}
      >
        <span className="select-value">{opts[index]?.label ?? value}</span>
        <span className="select-arrow" aria-hidden="true" />
      </button>
      {open
        ? createPortal(
            <div ref={popupRef} className="popup" role="listbox" style={style}>
              {opts.map((o) => (
                <button
                  key={o.value}
                  type="button"
                  role="option"
                  aria-selected={o.value === value}
                  className={`popup-item${o.value === value ? ' is-selected' : ''}`}
                  onClick={() => {
                    setOpen(false)
                    if (o.value !== value) onChange(o.value)
                  }}
                >
                  {o.label}
                </button>
              ))}
            </div>,
            document.body,
          )
        : null}
    </>
  )
}
