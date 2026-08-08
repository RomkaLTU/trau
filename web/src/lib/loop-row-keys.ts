import { isTyping, type KeyStroke } from './keys'
import { rowActionKey } from './roving-list'

export type LoopRowAction = 'select' | 'moveUp' | 'moveDown' | 'run' | 'remove'

const ACTIONS: Record<string, LoopRowAction> = {
  ' ': 'select',
  r: 'run',
  x: 'remove',
}

// Reordering rides Alt so the bare arrows keep walking the list: Alt+↑ and Alt+↓
// are the row's own Move up and Move down, and a stroke carrying any other
// modifier was never the row's.
function reorderAction(e: KeyStroke): LoopRowAction | null {
  if (e.isComposing || e.ctrlKey || e.metaKey || e.shiftKey) return null
  if (!e.altKey || isTyping(e)) return null
  if (e.key === 'ArrowUp') return 'moveUp'
  if (e.key === 'ArrowDown') return 'moveDown'
  return null
}

// Which of these a row can answer stays the row's own business — a settled row
// has no checkbox for Space and no order to move.
export function loopRowAction(e: KeyStroke): LoopRowAction | null {
  const reorder = reorderAction(e)
  if (reorder !== null) return reorder
  const key = rowActionKey(e)
  return key === null ? null : ACTIONS[key] ?? null
}
