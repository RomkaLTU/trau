// Assignee is an issue's assignee as the board and issue views see it: the tracker
// id and display name, plus whether it resolves to the repo's own identity. Me is
// computed server-side against the repo binding (ADR 0014) — the client only ever
// sees the boolean, and there are no avatar image URLs by design.
export interface Assignee {
  id: string
  name: string
  me: boolean
}

const AVATAR_TONES = [
  'bg-rose-600',
  'bg-orange-600',
  'bg-amber-600',
  'bg-emerald-600',
  'bg-teal-600',
  'bg-sky-600',
  'bg-blue-600',
  'bg-indigo-600',
  'bg-violet-600',
  'bg-fuchsia-600',
] as const

// assigneeLabel is the assignee's display name, collapsed to "Me" for the repo's
// own identity — never "You" (ADR 0014).
export function assigneeLabel(assignee: Assignee): string {
  return assignee.me ? 'Me' : assignee.name
}

export function avatarInitials(assignee: Assignee): string {
  return assignee.me ? 'Me' : initials(assignee.name)
}

export function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean)
  if (parts.length === 0) return '?'
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase()
  return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase()
}

// avatarTone picks a stable background class from the display name, so one person
// keeps the same colour across every surface with no stored palette.
export function avatarTone(name: string): string {
  let hash = 0
  for (let i = 0; i < name.length; i++) {
    hash = (hash * 31 + name.charCodeAt(i)) | 0
  }
  return AVATAR_TONES[Math.abs(hash) % AVATAR_TONES.length]
}
