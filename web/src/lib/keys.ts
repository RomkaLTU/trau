// KeyStroke is a keydown reduced to what a binding judges: the key and its modifiers,
// the tag focus sits on, and whether a layer covers the workspace. Listeners read those
// off the DOM so the decisions themselves stay pure functions.
export interface KeyStroke {
  key: string
  ctrlKey?: boolean
  metaKey?: boolean
  altKey?: boolean
  shiftKey?: boolean
  isComposing?: boolean
  targetTag?: string
  targetEditable?: boolean
  layerOpen?: boolean
  // Names the open layer only when exactly one covers the workspace, so a binding
  // that deliberately survives a named layer can recognize it; null under none and
  // under two, which no carve-out may pass.
  soleLayer?: string | null
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
    shiftKey: e.shiftKey,
    isComposing: e.isComposing,
    targetTag: target?.tagName,
    targetEditable: target?.isContentEditable,
    layerOpen: hasOpenLayer(document),
    soleLayer: soleOpenLayer(document),
  }
}

// isBareKey holds while the keystroke means nothing else already: a modified one
// belongs to the browser, and a composing one to the IME.
export function isBareKey(e: KeyStroke): boolean {
  return !e.isComposing && !e.ctrlKey && !e.metaKey && !e.altKey
}

// isUnshiftedKey is isBareKey with Shift counted too, for the bindings that walk a
// list: Shift+j and Shift+ArrowDown are a selection gesture the reader means for
// something else. The ? that opens the help dialog keeps isBareKey instead, since
// it rides Shift+/ on most layouts.
export function isUnshiftedKey(e: KeyStroke): boolean {
  return isBareKey(e) && !e.shiftKey
}

// isTyping holds while the keystroke is text going into a field or a contenteditable
// editor, which no binding may take from the user.
export function isTyping(e: KeyStroke): boolean {
  return e.targetEditable === true || TYPING_TAGS.includes(e.targetTag ?? '')
}

export function hasOpenLayer(doc: Document): boolean {
  return doc.querySelector(OPEN_LAYERS) !== null
}

// Only a layer that names itself can be carved out of the rule that an open layer
// owns the keyboard; an unnamed one keeps blocking every bare key.
export const LAYER_ATTR = 'data-key-layer'

export function soleOpenLayer(doc: Document): string | null {
  const layers = doc.querySelectorAll(OPEN_LAYERS)
  if (layers.length !== 1) return null
  return layers[0].getAttribute(LAYER_ATTR)
}
