import { useEffect, useLayoutEffect, useRef, useState, type CSSProperties } from 'react'

/** Gap between the anchor and what it opens, and between that and the window edge. */
const GAP = 4

/** A list that opens from an anchor (a menu, a dropdown). It is rendered in
 *  the document body and placed by hand, because the panels it opens from
 *  scroll and clip: below the anchor when it fits there, above it otherwise.
 *  It closes on a click outside, on Escape, and when what is under it moves. */
export function usePopup<A extends HTMLElement>(align: 'start' | 'end') {
  const [open, setOpen] = useState(false)
  const anchorRef = useRef<A>(null)
  const popupRef = useRef<HTMLDivElement>(null)
  const [style, setStyle] = useState<CSSProperties>({ visibility: 'hidden' })

  useLayoutEffect(() => {
    if (!open) {
      setStyle({ visibility: 'hidden' })
      return
    }
    const a = anchorRef.current?.getBoundingClientRect()
    const p = popupRef.current
    if (!a || !p) return
    const below = window.innerHeight - a.bottom - GAP * 2
    const above = a.top - GAP * 2
    const up = p.offsetHeight > below && above > below
    const width = Math.max(p.offsetWidth, align === 'start' ? a.width : 0)
    const left = Math.min(Math.max(GAP, align === 'end' ? a.right - width : a.left), window.innerWidth - width - GAP)
    setStyle({
      left,
      minWidth: align === 'start' ? a.width : undefined,
      maxHeight: up ? above : below,
      ...(up ? { bottom: window.innerHeight - a.top + GAP } : { top: a.bottom + GAP }),
    })
  }, [open, align])

  useEffect(() => {
    if (!open) return
    const inside = (t: EventTarget | null) => t instanceof Node && (anchorRef.current?.contains(t) || popupRef.current?.contains(t))
    const onDown = (e: MouseEvent) => {
      if (!inside(e.target)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    const onScroll = (e: Event) => {
      if (!popupRef.current?.contains(e.target as Node)) setOpen(false)
    }
    const close = () => setOpen(false)
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    document.addEventListener('scroll', onScroll, true)
    window.addEventListener('resize', close)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
      document.removeEventListener('scroll', onScroll, true)
      window.removeEventListener('resize', close)
    }
  }, [open])

  return { open, setOpen, anchorRef, popupRef, style }
}
