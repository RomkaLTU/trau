// KeyStroke is a keydown reduced to what a binding judges: the key and its modifiers,
// the tag focus sits on, and whether a layer covers the workspace. Listeners read those
// off the DOM so the decisions themselves stay pure functions.
export interface KeyStroke {
  key: string
  ctrlKey?: boolean
  metaKey?: boolean
  altKey?: boolean
  isComposing?: boolean
  targetTag?: string
  targetEditable?: boolean
  layerOpen?: boolean
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

export function readKeyStroke(e: KeyboardEvent): KeyStroke {
  const target = e.target as HTMLElement | null
  return {
    key: e.key,
    ctrlKey: e.ctrlKey,
    metaKey: e.metaKey,
    altKey: e.altKey,
    isComposing: e.isComposing,
    targetTag: target?.tagName,
    targetEditable: target?.isContentEditable,
    layerOpen: hasOpenLayer(document),
  }
}

// isBareKey holds while the keystroke means nothing else already: a modified one
// belongs to the browser, and a composing one to the IME.
export function isBareKey(e: KeyStroke): boolean {
  return !e.isComposing && !e.ctrlKey && !e.metaKey && !e.altKey
}

// isTyping holds while the keystroke is text going into a field or a contenteditable
// editor, which no binding may take from the user.
export function isTyping(e: KeyStroke): boolean {
  return e.targetEditable === true || TYPING_TAGS.includes(e.targetTag ?? '')
}

export function hasOpenLayer(doc: Document): boolean {
  return doc.querySelector(OPEN_LAYERS) !== null
}
