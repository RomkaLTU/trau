import {
  useCallback,
  useEffect,
  useRef,
  type KeyboardEvent as ReactKeyboardEvent,
  type RefObject,
} from 'react'

import { drawerKeyAction } from './drawer-keys'
import type { InboxItem } from './inbox'
import {
  inboxKeyAction,
  inboxMoveTarget,
  type InboxKeyAction,
} from './inbox-keys'
import { readKeyStroke } from './keys'
import {
  ROW_ATTR,
  rovingRows,
  useRovingList,
  type RovingRowProps,
} from './use-roving-list'

export interface InboxRail {
  /** The element the rows render inside; it owns every row binding. */
  listRef: RefObject<HTMLElement | null>
  listProps: {
    onKeyDown: (event: ReactKeyboardEvent<HTMLElement>) => void
    tabIndex: number
  }
  rowProps: (id: string) => RovingRowProps
  /** Props every control inside a row carries, so the rail keeps one Tab stop. */
  controlProps: { tabIndex: number }
}

// The inbox rail is one keyboard list whose Tab stop is the conversation on screen:
// the walk moves the selection, and the row focus and the scroll follow it rather
// than tracking a stop of their own. The queue's order is the walk's — the shared
// primitive supplies the focus, the Tab step into a row's controls, and Enter.
export function useInboxRail({
  items,
  selectedId,
  onSelect,
}: {
  items: readonly InboxItem[]
  selectedId: string | null
  onSelect: (id: string) => void
}): InboxRail {
  const listRef = useRef<HTMLElement | null>(null)
  const rows = useRovingList({ container: listRef, onActivate: onSelect })
  const { focusRow } = rows

  const move = useCallback(
    (action: InboxKeyAction) => {
      const to = inboxMoveTarget(items, selectedId, action)
      if (to !== null && to !== selectedId) onSelect(to)
    },
    [items, selectedId, onSelect],
  )

  // The document listener below outlives any one render, so it reads the current
  // walk rather than the one it was registered with.
  const latest = useRef(move)
  useEffect(() => {
    latest.current = move
  })

  // Focus follows the selection only while the walk is happening in the rail: an
  // advance applied from the composer moves the queue on without taking the keyboard
  // away from it, and the row is scrolled into view either way.
  useEffect(() => {
    const list = listRef.current
    if (!list || selectedId === null) return
    if (list.contains(document.activeElement)) {
      focusRow(selectedId)
      return
    }
    const row = rovingRows(list).find(
      (el) => el.getAttribute(ROW_ATTR) === selectedId,
    )
    row?.scrollIntoView({ block: 'nearest' })
  }, [selectedId, focusRow])

  // Reading happens in the conversation beside the rail, so the walk keys answer
  // from anywhere on the page — and from under an open ticket drawer, through the
  // same carve-out the board walks by. A keystroke the rail already handled arrives
  // here spent, since React's own handler runs first.
  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if (e.defaultPrevented) return
      const stroke = readKeyStroke(e)
      const action = inboxKeyAction(stroke) ?? drawerKeyAction(stroke)
      if (action === null) return
      e.preventDefault()
      latest.current(action)
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [])

  function onKeyDown(event: ReactKeyboardEvent<HTMLElement>) {
    const action = inboxKeyAction(readKeyStroke(event.nativeEvent))
    if (action === null) {
      rows.listProps.onKeyDown(event)
      return
    }
    event.preventDefault()
    move(action)
  }

  // The rail's one Tab stop is the selected row, so Tab into the queue lands on the
  // conversation being read rather than where the roving stop was last parked.
  function rowProps(id: string): RovingRowProps {
    const props = rows.rowProps(id)
    if (selectedId === null) return props
    return { ...props, tabIndex: selectedId === id ? 0 : -1 }
  }

  return {
    listRef,
    listProps: { onKeyDown, tabIndex: -1 },
    rowProps,
    controlProps: rows.controlProps,
  }
}
