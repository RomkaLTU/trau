import { isBareKey, isTyping, type KeyStroke } from './keys'

// DockKeyAction is what an open dock does with a keystroke: walk the sessions waiting,
// hand the keyboard to the answer box, abandon the one on screen, or collapse back to
// the tab.
export type DockKeyAction = 'next' | 'prev' | 'answer' | 'abandon' | 'collapse'

const PANEL_ACTIONS: Record<string, DockKeyAction> = {
  j: 'next',
  ArrowDown: 'next',
  k: 'prev',
  ArrowUp: 'prev',
  Enter: 'answer',
  x: 'abandon',
  Escape: 'collapse',
}

// isDockOpenKey matches the dock's one global binding. It is a bare letter, so
// everything a letter already means — a modifier's chord, an IME's composition, text in
// a field, a layer holding the keyboard — keeps it.
export function isDockOpenKey(e: KeyStroke): boolean {
  if (!isBareKey(e) || e.layerOpen || isTyping(e)) return false
  return e.key === 'i'
}

// dockPanelAction maps a keystroke inside the open panel to its binding. Escape is the
// one that still lands from the answer box — it is never text, and it leaves the dock
// the way the app's dialogs are left from a field they own; every other binding is a
// letter the box is owed.
export function dockPanelAction(e: KeyStroke): DockKeyAction | null {
  if (!isBareKey(e) || e.layerOpen) return null
  if (isTyping(e)) return e.key === 'Escape' ? 'collapse' : null
  return PANEL_ACTIONS[e.key] ?? null
}
