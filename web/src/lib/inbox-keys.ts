export type InboxKeyAction = 'next' | 'prev' | 'skip'

const ACTIONS: Record<string, InboxKeyAction> = {
  j: 'next',
  k: 'prev',
  s: 'skip',
}

const TYPING_TAGS = ['INPUT', 'TEXTAREA', 'SELECT']

// Radix keeps a layer mounted through its close animation, so the open one is the
// content still marked open; poppers — popover, select, command — are parked in a
// wrapper of their own instead. The app's own dropdowns are hand-rolled and mount
// their listbox only while open, so the role alone is the signal.
const OPEN_LAYERS = [
  '[role="dialog"][data-state="open"]',
  '[role="alertdialog"][data-state="open"]',
  '[data-radix-popper-content-wrapper]',
  '[role="listbox"]',
].join(',')

// InboxKeyEvent is a keydown reduced to what the bindings judge: the key and its
// modifiers, the tag focus sits on, and whether a layer covers the workspace. The
// route reads those off the DOM so the decision itself stays a pure function.
export interface InboxKeyEvent {
  key: string
  ctrlKey?: boolean
  metaKey?: boolean
  altKey?: boolean
  isComposing?: boolean
  targetTag?: string
  layerOpen?: boolean
}

// inboxKeyAction maps a bare keystroke to its queue action. Anything the keystroke
// could already mean is not ours: a modifier belongs to the browser, a composing
// keystroke to the IME, a letter over a field is text the user is typing, and an
// open dialog or popover owns the keyboard until it closes.
export function inboxKeyAction(e: InboxKeyEvent): InboxKeyAction | null {
  if (e.isComposing || e.ctrlKey || e.metaKey || e.altKey) return null
  if (e.layerOpen || TYPING_TAGS.includes(e.targetTag ?? '')) return null
  return ACTIONS[e.key] ?? null
}

export function hasOpenLayer(doc: Document): boolean {
  return doc.querySelector(OPEN_LAYERS) !== null
}
