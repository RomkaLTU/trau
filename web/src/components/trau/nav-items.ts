import {
  DollarSign,
  FlaskConical,
  FolderPlus,
  Globe,
  Inbox,
  LayoutDashboard,
  Lightbulb,
  ListChecks,
  ListTodo,
  Network,
  Puzzle,
  RefreshCw,
  Server,
  Settings,
  SquareTerminal,
  type LucideIcon,
} from 'lucide-react'

export interface NavItem {
  label: string
  icon: LucideIcon
  to: string
  search?: Record<string, string>
  exact?: boolean
  attention?: boolean
  loop?: boolean
  /** Show the triage inbox count — total, with the awaiting-answer count emphasized. */
  inbox?: boolean
  /** Light up while a research session is working. */
  research?: boolean
  /** Page acts on a single repo — the link is disabled under "All projects". */
  requiresProject?: boolean
}

export interface NavGroup {
  label: string
  items: NavItem[]
}

export const NAV_GROUPS: NavGroup[] = [
  {
    label: 'OPERATE',
    items: [
      {
        label: 'Overview',
        icon: LayoutDashboard,
        to: '/',
        exact: true,
        attention: true,
      },
      {
        label: 'Loop',
        icon: RefreshCw,
        to: '/loop',
        requiresProject: true,
        loop: true,
      },
      { label: 'Backlog', icon: ListTodo, to: '/backlog', requiresProject: true },
      {
        label: 'Inbox',
        icon: Inbox,
        to: '/inbox',
        requiresProject: true,
        inbox: true,
      },
      {
        label: 'Research',
        icon: FlaskConical,
        to: '/research',
        requiresProject: true,
        research: true,
      },
    ],
  },
  {
    label: 'OBSERVE',
    items: [
      { label: 'Runs', icon: ListChecks, to: '/runs' },
      {
        label: 'Terminal',
        icon: SquareTerminal,
        to: '/terminal',
        requiresProject: true,
      },
      { label: 'Atlas', icon: Network, to: '/atlas', requiresProject: true },
      { label: 'Costs', icon: DollarSign, to: '/costs' },
      { label: 'Lessons', icon: Lightbulb, to: '/lessons' },
    ],
  },
  {
    label: 'CONFIGURE',
    items: [
      { label: 'New project', icon: FolderPlus, to: '/projects/new' },
      { label: 'Skills', icon: Puzzle, to: '/skills' },
      { label: 'Settings', icon: Settings, to: '/settings' },
      { label: 'Hub', icon: Server, to: '/hub' },
    ],
  },
]

/** Pages the palette and in-page links reach, but the sidebar deliberately doesn't list. */
export const UNLISTED_ITEMS: NavItem[] = [
  { label: 'App URLs', icon: Globe, to: '/app-urls', requiresProject: true },
]

export interface GTarget {
  /** The second key of the `g` sequence. */
  key: string
  item: NavItem
}

const G_LABELS: [string, string][] = [
  ['o', 'Overview'],
  ['l', 'Loop'],
  ['b', 'Backlog'],
  ['i', 'Inbox'],
  ['e', 'Research'],
  ['r', 'Runs'],
  ['t', 'Terminal'],
  ['a', 'Atlas'],
  ['c', 'Costs'],
  ['n', 'Lessons'],
  ['k', 'Skills'],
  ['s', 'Settings'],
  ['h', 'Hub'],
]

// G_TARGETS is the g-sequence route map. Each jump points at the sidebar's own
// nav item, so it carries that page's route, search, and project gate.
export const G_TARGETS: GTarget[] = (() => {
  const byLabel = new Map(
    NAV_GROUPS.flatMap((group) => group.items).map((item) => [item.label, item]),
  )
  return G_LABELS.flatMap(([key, label]) => {
    const item = byLabel.get(label)
    return item ? [{ key, item }] : []
  })
})()

export const G_TARGET_KEYS: ReadonlySet<string> = new Set(
  G_TARGETS.map((target) => target.key),
)

export function gTarget(key: string): NavItem | null {
  return G_TARGETS.find((target) => target.key === key)?.item ?? null
}
