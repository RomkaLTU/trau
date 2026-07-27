export type Box = Pick<DOMRectReadOnly, 'top' | 'right' | 'bottom' | 'left'>

// A hidden input takes no keyboard and a disabled editor drops the attribute; the rest
// of the input types stay in, since the tab is as much in the way of the checkbox the
// user is on as of the field they are typing in.
const EDITABLE = 'input:not([type="hidden"]), textarea, [contenteditable="true"]'

// shouldDodge asks whether the dock's tab is sitting on what the user is typing in. The
// box to judge against is always the full tab's — the collapsed pill's own, being
// smaller, would clear the overlap the moment it took the corner and flip the tab
// straight back over the field.
export function shouldDodge(focused: Element | null, tab: Box | null): boolean {
  if (!tab || !focused?.matches(EDITABLE)) return false

  const field = focused.getBoundingClientRect()
  return (
    field.left < tab.right &&
    field.right > tab.left &&
    field.top < tab.bottom &&
    field.bottom > tab.top
  )
}
