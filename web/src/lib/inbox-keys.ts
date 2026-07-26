import { isBareKey, isTyping, type KeyStroke } from './keys'

export type InboxKeyAction = 'next' | 'prev' | 'skip'

const ACTIONS: Record<string, InboxKeyAction> = {
  j: 'next',
  k: 'prev',
  s: 'skip',
}

// inboxKeyAction maps a bare keystroke to its queue action. Anything the keystroke
// could already mean is not ours: a modifier belongs to the browser, a composing
// keystroke to the IME, a letter over a field or contenteditable editor is text
// the user is typing, and an open dialog or popover owns the keyboard until it
// closes.
export function inboxKeyAction(e: KeyStroke): InboxKeyAction | null {
  if (!isBareKey(e) || e.layerOpen || isTyping(e)) return null
  return ACTIONS[e.key] ?? null
}
