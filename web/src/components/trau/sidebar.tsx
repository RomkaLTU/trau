import { useQuery } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import {
  DollarSign,
  FolderPlus,
  Inbox,
  LayoutDashboard,
  Lightbulb,
  ListChecks,
  ListTodo,
  Lock,
  Network,
  Play,
  Puzzle,
  RefreshCw,
  Settings,
  SquareTerminal,
  type LucideIcon,
} from 'lucide-react'

import { useActiveRepo } from '@/components/trau/active-repo'
import { GlobalSearch } from '@/components/trau/global-search'
import { RepoSwitcher } from '@/components/trau/repo-switcher'
import { ThemeToggle } from '@/components/trau/theme-toggle'
import { useAttentionCount } from '@/lib/attention'
import { healthQueryOptions } from '@/lib/health'
import { useInboxCounts } from '@/lib/inbox'
import { cn } from '@/lib/utils'

interface NavItem {
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

interface NavGroup {
  label: string
  items: NavItem[]
}

const GROUPS: NavGroup[] = [
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
      { label: 'Run once', icon: Play, to: '/run-once', requiresProject: true },
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

interface NavBadge {
  count: number
  tone: 'warn' | 'muted'
  title?: string
}

// navBadge resolves an item's count pill: the attention item counts faulted runs;
// the inbox item shows its total, but emphasizes the parked-awaiting-answer count
// as the warn-toned "a question is waiting for you" signal when there is one.
function navBadge(
  item: NavItem,
  attention: number,
  inbox: { total: number; awaiting: number },
): NavBadge | null {
  if (item.attention) {
    return attention > 0 ? { count: attention, tone: 'warn' } : null
  }
  if (item.inbox) {
    if (inbox.awaiting > 0) {
      return {
        count: inbox.awaiting,
        tone: 'warn',
        title: `${inbox.awaiting} question${inbox.awaiting === 1 ? '' : 's'} waiting on you`,
      }
    }
    if (inbox.total > 0) {
      return {
        count: inbox.total,
        tone: 'muted',
        title: `${inbox.total} issue${inbox.total === 1 ? '' : 's'} to triage`,
      }
    }
  }
  return null
}

export function Sidebar() {
  const { repo, isAll, autoScope, openSwitcher } = useActiveRepo()
  const navigate = useNavigate()
  const attention = useAttentionCount(repo)
  const inbox = useInboxCounts(repo ?? '')

  // A gated nav click auto-scopes to a lone/last-used repo and follows the link,
  // or opens the switcher when there's a real choice — never a dead end.
  function recoverTo(item: NavItem) {
    if (autoScope()) {
      void navigate({ to: item.to, search: item.search })
    } else {
      openSwitcher()
    }
  }
  const host = window.location.host
  const health = useQuery(healthQueryOptions)
  const webVersion = `${__WEB_VERSION__}·${__WEB_BUILD__}`
  const cliVersion = health.data?.version

  return (
    <aside className="fixed inset-y-0 left-0 z-10 flex w-60 flex-col border-r border-border bg-card">
      <div className="flex flex-col gap-3 border-b border-border px-3 py-3">
        <Link
          to="/"
          className="px-1 font-mono text-lg font-medium tracking-tight text-foreground"
        >
          trau
          <span className="cursor-block text-primary">▍</span>
        </Link>
        <RepoSwitcher />
        <GlobalSearch />
      </div>

      <nav className="flex-1 overflow-y-auto px-3 py-2">
        {GROUPS.map((group) => (
          <div key={group.label} className="mb-5">
            <p className="px-2 pb-1.5 font-mono text-[0.65rem] uppercase tracking-[0.2em] text-muted-foreground">
              {group.label}
            </p>
            <ul className="flex flex-col gap-0.5">
              {group.items.map((item) => {
                const badge = navBadge(item, attention, inbox)
                const disabled = isAll && item.requiresProject

                if (disabled) {
                  return (
                    <li key={item.label}>
                      <button
                        type="button"
                        onClick={() => recoverTo(item)}
                        aria-disabled="true"
                        title="Select a project to use this page"
                        className="group relative flex w-full items-center gap-2.5 rounded-md px-2 py-1.5 text-left font-mono text-sm text-muted-foreground/40 transition-colors hover:text-muted-foreground/70"
                      >
                        <item.icon className="size-4" aria-hidden="true" />
                        <span className="flex-1">{item.label}</span>
                        <Lock className="size-3" aria-hidden="true" />
                      </button>
                    </li>
                  )
                }

                return (
                  <li key={item.label}>
                    <Link
                      to={item.to}
                      search={item.search}
                      activeOptions={{ exact: item.exact ?? false }}
                      className="group relative flex items-center gap-2.5 rounded-md px-2 py-1.5 font-mono text-sm text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
                      activeProps={{
                        className:
                          'bg-primary/10 text-primary hover:bg-primary/10 hover:text-primary',
                      }}
                    >
                      {({ isActive }) => (
                        <>
                          {isActive && (
                            <span
                              aria-hidden="true"
                              className="absolute inset-y-1.5 left-0 w-0.5 rounded-full bg-primary"
                            />
                          )}
                          <item.icon className="size-4" aria-hidden="true" />
                          <span className="flex-1">{item.label}</span>
                          {badge ? (
                            <span
                              title={badge.title}
                              className={cn(
                                'inline-flex h-4 min-w-4 items-center justify-center rounded-full border px-1 font-mono text-[0.65rem]',
                                badge.tone === 'warn'
                                  ? 'border-warn/50 bg-warn/12 text-warn'
                                  : 'border-border bg-muted/60 text-muted-foreground',
                              )}
                            >
                              {badge.count}
                            </span>
                          ) : null}
                        </>
                      )}
                    </Link>
                  </li>
                )
              })}
            </ul>
          </div>
        ))}
      </nav>

      <div className="flex flex-col gap-3 border-t border-border px-5 py-3">
        <div className="flex items-center justify-between gap-2">
          <span className="font-mono text-[0.65rem] uppercase tracking-[0.2em] text-muted-foreground">
            theme
          </span>
          <ThemeToggle />
        </div>
        <span className="inline-flex items-center gap-2 font-mono text-xs text-teal">
          <span aria-hidden="true">●</span>
          {host ? `serving ${host}` : 'serving'}
        </span>
        <dl className="grid grid-cols-[2rem_1fr] gap-x-2 font-mono text-[0.65rem] leading-relaxed text-muted-foreground">
          <dt className="text-muted-foreground/60">web</dt>
          <dd className="truncate text-foreground/75" title={webVersion}>
            {webVersion}
          </dd>
          <dt className="text-muted-foreground/60">cli</dt>
          <dd
            className="truncate text-foreground/75"
            title={cliVersion ?? 'unavailable'}
          >
            {cliVersion ?? '—'}
          </dd>
        </dl>
      </div>
    </aside>
  )
}
