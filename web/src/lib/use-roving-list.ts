import {
  useCallback,
  useLayoutEffect,
  useState,
  type KeyboardEvent,
  type RefObject,
} from 'react'

import { isTyping, readKeyStroke } from './keys'
import { activatesRow, rowMoveIndex } from './roving-list'

// Row order is DOM order, read at the keystroke rather than held in state: rows
// arrive from more than one query — a section's own page and each expanded
// parent's children — and only the DOM knows what they ended up rendering.
export const ROW_ATTR = 'data-roving-row'

// The Tab stop is held by a key rather than by a row id, because a list may render
// one row twice — a child of an expanded parent also stands in its own section —
// and two rows answering to the same id would be two Tab stops. A caller that can
// render an id twice passes a key that tells the two renderings apart; the rest
// let the id serve as its own key.
export const ROW_KEY_ATTR = 'data-roving-key'

const CONTROLS = 'button, a[href], input, select, textarea, [tabindex]'

export function rovingRows(container: HTMLElement | null): HTMLElement[] {
  if (!container) return []
  return [...container.querySelectorAll<HTMLElement>(`[${ROW_ATTR}]`)]
}

export function rovingRowIds(container: HTMLElement | null): string[] {
  return rovingRows(container).map(rowId)
}

function rowId(row: HTMLElement): string {
  return row.getAttribute(ROW_ATTR) ?? ''
}

function rowKey(row: HTMLElement): string {
  return row.getAttribute(ROW_KEY_ATTR) ?? rowId(row)
}

function rowControls(row: HTMLElement): HTMLElement[] {
  return [...row.querySelectorAll<HTMLElement>(CONTROLS)].filter(
    (el) => !el.hasAttribute('disabled'),
  )
}

export interface RovingRowProps {
  tabIndex: number
  onFocus: () => void
  [ROW_ATTR]: string
  [ROW_KEY_ATTR]: string
}

export interface RovingList {
  /** Props for the element the rows render inside; it owns every row binding. */
  listProps: {
    onKeyDown: (event: KeyboardEvent<HTMLElement>) => void
    tabIndex: number
  }
  /**
   * Props for one rendered row. key tells two renderings of the same id apart and
   * defaults to the id, which is enough for a list that renders each row once.
   */
  rowProps: (id: string, key?: string) => RovingRowProps
  /** Props every control inside a row carries, so the list keeps one Tab stop. */
  controlProps: { tabIndex: number }
  /** focusRow moves focus to a row, answering false when the list has no such row. */
  focusRow: (id: string) => boolean
  /** focusTabStop lands focus back on the list after a layer over it closes. */
  focusTabStop: () => void
  /** focused is the key of the row holding the Tab stop. */
  focused: string | null
}

// One Tab stop over the whole list, arrows and bare j/k to walk it, Home/End to
// the ends, Enter to open, Tab to step into the focused row's own controls. The
// handler sits on the container, not the row, so the arrows keep moving rows
// while focus is on a control inside one.
export function useRovingList({
  container,
  onActivate,
}: {
  container: RefObject<HTMLElement | null>
  onActivate?: (id: string) => void
}): RovingList {
  const [focused, setFocused] = useState<string | null>(null)

  // The Tab stop parks on the first row and returns there when the row it sat on
  // leaves — archived, filtered away, paged past. Any render may change the rows,
  // so this runs on all of them, before a paint could show a list holding none.
  useLayoutEffect(() => {
    const keys = rovingRows(container.current).map(rowKey)
    if (keys.length === 0) {
      if (focused !== null) setFocused(null)
      return
    }
    if (focused === null || !keys.includes(focused)) setFocused(keys[0])
  })

  // focusStop takes the row element rather than an id: a row rendered twice sends
  // focus to the neighbour the reader saw, not to the other copy of itself.
  const focusStop = useCallback((row: HTMLElement) => {
    setFocused(rowKey(row))
    row.focus()
    row.scrollIntoView({ block: 'nearest' })
  }, [])

  const focusRow = useCallback(
    (id: string) => {
      const row = rovingRows(container.current).find((el) => rowId(el) === id)
      if (!row) return false
      focusStop(row)
      return true
    },
    [container, focusStop],
  )

  // Somewhere real to put focus when a layer over the list closes: the row still
  // holding the Tab stop, or the list itself while it has no rows at all.
  function focusTabStop() {
    const row = rovingRows(container.current).find((el) => rowKey(el) === focused)
    if (row) {
      focusStop(row)
      return
    }
    container.current?.focus()
  }

  function onKeyDown(event: KeyboardEvent<HTMLElement>) {
    const target = event.target as HTMLElement
    const row = target.closest<HTMLElement>(`[${ROW_ATTR}]`)
    if (!row) return
    const rows = rovingRows(container.current)
    const at = rows.indexOf(row)
    if (at === -1) return
    const stroke = readKeyStroke(event.nativeEvent)

    const to = rowMoveIndex(stroke, at, rows.length)
    if (to !== null) {
      event.preventDefault()
      focusStop(rows[to])
      return
    }

    if (event.key === 'Tab' && !isTyping(stroke)) {
      const controls = rowControls(row)
      const from = controls.indexOf(target)
      const next = from + (event.shiftKey ? -1 : 1)
      // Off either end the browser takes over, which is what leaves the list.
      if (from === -1 && event.shiftKey) return
      if (next >= controls.length) return
      event.preventDefault()
      if (next < 0) row.focus()
      else controls[next].focus()
      return
    }

    // Below here is the row's own: Enter on a control inside it is that control's.
    if (target !== row) return
    // A row that is itself a link is opened by the browser, so Enter is claimed
    // only where the caller has an activation of its own.
    if (onActivate && activatesRow(stroke)) {
      event.preventDefault()
      onActivate(rowId(row))
    }
  }

  return {
    // The container takes focus only when handed it: -1 keeps the list's one Tab
    // stop on a row.
    listProps: { onKeyDown, tabIndex: -1 },
    rowProps: (id: string, key: string = id) => ({
      tabIndex: focused === key ? 0 : -1,
      onFocus: () => setFocused(key),
      [ROW_ATTR]: id,
      [ROW_KEY_ATTR]: key,
    }),
    controlProps: { tabIndex: -1 },
    focusRow,
    focusTabStop,
    focused,
  }
}
