import { useRef, type KeyboardEvent } from 'react'

import { readKeyStroke } from './keys'
import { groupMoveIndex } from './roving-group'

export interface RovingItemProps {
  tabIndex: number
  ref: (el: HTMLElement | null) => void
}

export interface RovingGroup {
  /** Props for the element carrying role="tablist" or role="radiogroup". */
  groupProps: { onKeyDown: (event: KeyboardEvent<HTMLElement>) => void }
  /** Props for one item, keyed by the value it stands for. */
  itemProps: (key: string) => RovingItemProps
}

// The keyboard contract role="tablist" and role="radiogroup" announce: one Tab
// stop for the whole group, arrows walking it and selecting as they go, Home and
// End to the ends. The Tab stop rides the selection so Tab returns a reader to
// the item they left, falling back to the first while nothing is selected.
export function useRovingGroup({
  keys,
  selected,
  onSelect,
}: {
  keys: readonly string[]
  selected: string | null
  onSelect: (key: string) => void
}): RovingGroup {
  const items = useRef(new Map<string, HTMLElement>())
  const stop = selected !== null && keys.includes(selected) ? selected : keys[0]

  function onKeyDown(event: KeyboardEvent<HTMLElement>) {
    // The handler sits on the group, so a keystroke from anything else inside it
    // — the name field the "New project…" option opens — is not the group's.
    const at = keys.findIndex((key) => items.current.get(key) === event.target)
    if (at === -1) return
    const to = groupMoveIndex(
      readKeyStroke(event.nativeEvent),
      at,
      keys.length,
    )
    if (to === null) return
    event.preventDefault()
    // Focus lands before the selection so the item is still the one the reader
    // moved to, whatever the selection does to the group on its way through.
    items.current.get(keys[to])?.focus()
    onSelect(keys[to])
  }

  return {
    groupProps: { onKeyDown },
    itemProps: (key: string) => ({
      tabIndex: key === stop ? 0 : -1,
      ref: (el: HTMLElement | null) => {
        if (el) items.current.set(key, el)
        else items.current.delete(key)
      },
    }),
  }
}
