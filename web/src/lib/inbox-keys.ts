import { nextIssueId, prevIssueId, type InboxItem } from './inbox'
import { isTyping, isUnshiftedKey, type KeyStroke } from './keys'

export type InboxKeyAction = 'next' | 'prev' | 'first' | 'last'

const ACTIONS: Record<string, InboxKeyAction> = {
  j: 'next',
  ArrowDown: 'next',
  k: 'prev',
  ArrowUp: 'prev',
  Home: 'first',
  End: 'last',
}

// inboxKeyAction maps a keystroke to its walk over the queue. Anything the keystroke
// could already mean is not ours: a modifier — Shift included, since Shift+↓ is a
// selection gesture — belongs to the browser, a composing keystroke to the IME, a
// letter over a field or contenteditable editor is text the user is typing, and an
// open dialog or popover owns the keyboard until it closes. The one layer that gives
// the keys back is the ticket drawer, which the caller reads with drawerKeyAction.
export function inboxKeyAction(e: KeyStroke): InboxKeyAction | null {
  if (!isUnshiftedKey(e) || e.layerOpen || isTyping(e)) return null
  return ACTIONS[e.key] ?? null
}

// inboxMoveTarget is where a walk key lands. It steps the queue's own order rather
// than the rail's rendering, so the Done today rows below the walk stay a pointer's
// business. Null means nowhere to go and the selection stays where it is.
export function inboxMoveTarget(
  items: readonly InboxItem[],
  id: string | null,
  action: InboxKeyAction,
): string | null {
  if (items.length === 0) return null
  if (action === 'first') return items[0].id
  if (action === 'last') return items[items.length - 1].id
  if (id === null) return items[0].id
  return action === 'next' ? nextIssueId(items, id) : prevIssueId(items, id)
}
