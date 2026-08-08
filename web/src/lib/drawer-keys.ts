import { isTyping, isUnshiftedKey, type KeyStroke } from './keys'

export const TICKET_DRAWER_LAYER = 'ticket-drawer'

export type DrawerKeyAction = 'next' | 'prev'

const ACTIONS: Record<string, DrawerKeyAction> = {
  j: 'next',
  k: 'prev',
}

// A deliberate carve-out in the rule that an open layer owns the keyboard: the
// drawer reads the row under it rather than holding a task of its own, so j and k
// keep walking the list beneath while it is the only layer on screen. A second
// layer over it takes the keys straight back.
export function drawerKeyAction(e: KeyStroke): DrawerKeyAction | null {
  if (!isUnshiftedKey(e) || isTyping(e)) return null
  if (e.soleLayer !== TICKET_DRAWER_LAYER) return null
  return ACTIONS[e.key] ?? null
}
