// DeleteTarget is what the confirm needs to know about the issue it is about to
// purge: whether it is a synced tracker ticket, and how many children go with it.
// The count is required — a confirm that does not yet know the blast radius has
// nothing to warn about and must not be shown.
export interface DeleteTarget {
  id: string
  source?: string
  children: number
}

const PERMANENCE =
  'Deletes the conversation and every local record — messages, attachments, notifications, queue entries.'

const NEVER_RESYNC =
  'It will never sync into this repo again. The issue on the tracker is not touched.'

export function deleteWarning(target: DeleteTarget): string {
  const parts = [PERMANENCE]
  if (target.source !== 'internal') parts.push(NEVER_RESYNC)
  if (target.children > 0) {
    const noun = target.children === 1 ? 'child' : 'children'
    parts.push(`Also deletes its ${target.children} ${noun}.`)
  }
  return parts.join(' ')
}

export function deleteToastMessage(id: string, deleted: readonly string[]): string {
  const children = deleted.filter((gone) => gone !== id).length
  if (children === 0) return `${id} deleted`
  const noun = children === 1 ? 'child' : 'children'
  return `${id} and ${children} ${noun} deleted`
}
