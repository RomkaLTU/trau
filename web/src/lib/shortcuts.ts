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

const LISTS: Shortcut[] = [
  { keys: ['j'], label: 'Next issue', group: 'Lists', where: 'Inbox' },
  { keys: ['k'], label: 'Previous issue', group: 'Lists', where: 'Inbox' },
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
