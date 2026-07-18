import {
  DollarSign,
  FolderPlus,
  Inbox,
  LayoutDashboard,
  Lightbulb,
  ListChecks,
  ListTodo,
  Network,
  Puzzle,
  RefreshCw,
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
  /** Show the triage inbox count — total, with the awaiting-answer count emphasized. */
  inbox?: boolean
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
      { label: 'Loop', icon: RefreshCw, to: '/loop', requiresProject: true },
      { label: 'Backlog', icon: ListTodo, to: '/backlog', requiresProject: true },
      {
        label: 'Inbox',
        icon: Inbox,
        to: '/inbox',
        requiresProject: true,
        inbox: true,
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
      { label: 'Add a project', icon: FolderPlus, to: '/projects/new' },
      { label: 'Skills', icon: Puzzle, to: '/skills' },
      { label: 'Settings', icon: Settings, to: '/settings' },
    ],
  },
]
