import { createPortal } from 'react-dom'
import { usePopup } from './usePopup'

export interface MenuItem {
  label: string
  onSelect(): void
  danger?: boolean
  disabled?: boolean
  title?: string
}

/** The three dots at the end of a row and the small vertical menu they open. */
export function DotsMenu({ label, items, disabled }: { label: string; items: MenuItem[]; disabled?: boolean }) {
  const { open, setOpen, anchorRef, popupRef, style } = usePopup<HTMLButtonElement>('end')
  return (
    <>
      <button
        ref={anchorRef}
        type="button"
        className={`dots${open ? ' is-open' : ''}`}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={label}
        title={open ? undefined : label}
        disabled={disabled}
        onClick={() => setOpen(!open)}
      >
        ⋯
      </button>
      {open
        ? createPortal(
            <div ref={popupRef} className="popup" role="menu" style={style}>
              {items.map((it) => (
                <button
                  key={it.label}
                  type="button"
                  role="menuitem"
                  className={`popup-item${it.danger ? ' is-danger' : ''}`}
                  disabled={it.disabled}
                  title={it.title}
                  onClick={() => {
                    setOpen(false)
                    it.onSelect()
                  }}
                >
                  {it.label}
                </button>
              ))}
            </div>,
            document.body,
          )
        : null}
    </>
  )
}
