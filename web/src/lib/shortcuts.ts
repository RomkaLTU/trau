import { G_TARGETS } from '@/components/trau/nav-items'

export const SHORTCUT_GROUPS = [
  'Global',
  'Navigation',
  'Lists',
  'Palette',
] as const

export type ShortcutGroup = (typeof SHORTCUT_GROUPS)[number]

export interface Shortcut {
  /** Keys in press order. 'mod' stands for ⌘ on a Mac and Ctrl everywhere else. */
  keys: string[]
  /** A second key bound to the same thing, listed beside the first as an "or". */
  alt?: string[]
  label: string
  group: ShortcutGroup
  /** The surface the binding applies to. */
  where: string
}

const GLOBAL: Shortcut[] = [
  {
    keys: ['mod', 'K'],
    label: 'Open the command palette',
    group: 'Global',
    where: 'Anywhere',
  },
  {
    keys: ['mod', 'P'],
    label: 'Switch project',
    group: 'Global',
    where: 'Anywhere',
  },
  {
    keys: ['?'],
    label: 'Show keyboard shortcuts',
    group: 'Global',
    where: 'Anywhere',
  },
]

const NAVIGATION: Shortcut[] = G_TARGETS.map((target) => ({
  keys: ['g', target.key],
  label: `Go to ${target.item.label}`,
  group: 'Navigation',
  where: 'Anywhere',
}))

// Every roving list is walked the same way, so the walk is declared once per
// surface and only the row's own actions are spelled out beside it. A surface
// whose rows are a bare link has nothing for Tab to reach, so it drops that step.
function rowWalk(where: string, open: string, stepIn = true): Shortcut[] {
  const walk: Shortcut[] = [
    { keys: ['↓'], alt: ['j'], label: 'Next row', group: 'Lists', where },
    { keys: ['↑'], alt: ['k'], label: 'Previous row', group: 'Lists', where },
    { keys: ['Home'], label: 'First row', group: 'Lists', where },
    { keys: ['End'], label: 'Last row', group: 'Lists', where },
    { keys: ['Enter'], label: open, group: 'Lists', where },
  ]
  if (!stepIn) return walk
  return [
    ...walk,
    {
      keys: ['Tab'],
      label: "Step into the row's own actions",
      group: 'Lists',
      where,
    },
  ]
}

const LISTS: Shortcut[] = [
  ...rowWalk('Inbox', 'Open the selected issue'),
  {
    keys: ['Enter'],
    label: 'Send the message',
    group: 'Lists',
    where: 'Inbox composer',
  },
  ...rowWalk('Backlog', 'Open the ticket'),
  {
    keys: ['e'],
    label: 'Expand or collapse the row',
    group: 'Lists',
    where: 'Backlog',
  },
  { keys: ['a'], label: 'Archive the row', group: 'Lists', where: 'Backlog' },
  ...rowWalk('Loop', 'Open the ticket, or a finished row’s run'),
  {
    keys: ['Space'],
    label: 'Include the row in a batch',
    group: 'Lists',
    where: 'Loop',
  },
  {
    keys: ['Alt', '↑'],
    label: 'Move the row up the queue',
    group: 'Lists',
    where: 'Loop',
  },
  {
    keys: ['Alt', '↓'],
    label: 'Move the row down the queue',
    group: 'Lists',
    where: 'Loop',
  },
  { keys: ['r'], label: 'Run the row now', group: 'Lists', where: 'Loop' },
  {
    keys: ['x'],
    label: 'Remove the row from the queue',
    group: 'Lists',
    where: 'Loop',
  },
  ...rowWalk('Runs', 'Open the run', false),
  {
    keys: ['j'],
    label: 'Next ticket',
    group: 'Lists',
    where: 'Ticket drawer',
  },
  {
    keys: ['k'],
    label: 'Previous ticket',
    group: 'Lists',
    where: 'Ticket drawer',
  },
]

const PALETTE: Shortcut[] = [
  {
    keys: ['Tab'],
    label: "Open the highlighted ticket's actions",
    group: 'Palette',
    where: 'Command palette',
  },
]

// SHORTCUTS is the one registry every binding is declared in, and the only
// source the shortcuts dialog renders.
export const SHORTCUTS: Shortcut[] = [
  ...GLOBAL,
  ...NAVIGATION,
  ...LISTS,
  ...PALETTE,
]

export function keyLabel(key: string, mac: boolean): string {
  if (key !== 'mod') return key
  return mac ? '⌘' : 'Ctrl'
}

export interface ShortcutSection {
  group: ShortcutGroup
  items: Shortcut[]
}

export function shortcutSections(
  list: readonly Shortcut[] = SHORTCUTS,
): ShortcutSection[] {
  return SHORTCUT_GROUPS.flatMap((group) => {
    const items = list.filter((shortcut) => shortcut.group === group)
    return items.length > 0 ? [{ group, items }] : []
  })
}
