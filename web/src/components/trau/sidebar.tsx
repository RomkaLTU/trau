import { useQuery } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import { Lock, Search } from 'lucide-react'

import { useActiveRepo } from '@/components/trau/active-repo'
import { NAV_GROUPS, type NavItem } from '@/components/trau/nav-items'
import { NotificationBell } from '@/components/trau/notification-bell'
import { RepoSwitcher } from '@/components/trau/repo-switcher'
import { ThemeToggle } from '@/components/trau/theme-toggle'
import { useAttentionCount } from '@/lib/attention'
import { healthQueryOptions } from '@/lib/health'
import { useInboxCounts } from '@/lib/inbox'
import { isMacPlatform, shortcutLabel } from '@/lib/palette-keys'
import { useProjectRepo } from '@/lib/projects'
import {
  queueQueryOptions,
  queueTerminal,
  type QueueResponse,
} from '@/lib/queue'
import {
  needsAttention,
  updateQueryOptions,
  versionLabel,
  type UpdateStatus,
} from '@/lib/update'
import { cn } from '@/lib/utils'

interface NavBadge {
  count: number
  tone: 'active' | 'warn' | 'muted'
  title?: string
}

export function navBadge(
  item: NavItem,
  attention: number,
  inbox: { total: number; awaiting: number },
  queue?: QueueResponse,
): NavBadge | null {
  if (item.attention) {
    return attention > 0 ? { count: attention, tone: 'warn' } : null
  }
  if (item.loop) {
    const count =
      queue?.items.filter((entry) => !queueTerminal(entry.status)).length ?? 0
    if (count === 0) return null
    const running =
      queue?.draining === true ||
      queue?.items.some((entry) => entry.status === 'running')
    const noun = count === 1 ? 'item' : 'items'
    return {
      count,
      tone: running ? 'active' : 'muted',
      title: `${count} ${noun} in queue${running ? ' — running' : ''}`,
    }
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

// updateHint names what the footer dot is about. Drift wins over a newer
// release: the binary is already here, and restarting is the shorter step.
function updateHint(status: UpdateStatus | undefined): string | null {
  if (!status) return null
  if (status.restartPending) {
    return `${versionLabel(status.onDisk)} is installed — restart the hub to apply it`
  }
  if (status.updateAvailable) {
    return `${versionLabel(status.latest)} is available`
  }
  return null
}

export function Sidebar({
  onOpenPalette,
  onOpenSwitcher,
}: {
  onOpenPalette: () => void
  onOpenSwitcher: () => void
}) {
  const { repo, repos, isAll, autoScope, openSwitcher } = useActiveRepo()
  const navigate = useNavigate()
  const attention = useAttentionCount(repo)
  const inboxRepo = useProjectRepo(repo ?? '', repos)
  const inbox = useInboxCounts(inboxRepo)
  const queue = useQuery({
    ...queueQueryOptions(repo ?? ''),
    refetchInterval: 3000,
  })

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
  const update = useQuery(updateQueryOptions)
  const webVersion = `${__WEB_VERSION__}·${__WEB_BUILD__}`
  const cliVersion = update.data?.running ?? health.data?.version
  const cliLabel = cliVersion ? versionLabel(cliVersion) : null

  return (
    <aside className="fixed inset-y-0 left-0 z-10 flex w-60 flex-col border-r border-border bg-card">
      <div className="flex flex-col gap-3 border-b border-border px-3 py-3">
        <div className="flex items-center justify-between">
          <Link
            to="/"
            className="px-1 font-mono text-lg font-medium tracking-tight text-foreground"
          >
            trau
            <span className="cursor-block text-primary">▍</span>
          </Link>
          <NotificationBell />
        </div>
        <RepoSwitcher onOpen={onOpenSwitcher} />
        <button
          type="button"
          onClick={onOpenPalette}
          title="Open command palette"
          className="group flex items-center gap-2 rounded-md border border-border bg-card px-2.5 py-1.5 transition-colors hover:border-ring/50 dark:bg-input"
        >
          <Search
            className="size-3.5 shrink-0 text-muted-foreground"
            aria-hidden="true"
          />
          <span className="flex-1 text-left font-mono text-sm text-muted-foreground transition-colors group-hover:text-foreground">
            Search…
          </span>
          <kbd className="shrink-0 rounded border border-border px-1 py-0.5 font-mono text-[0.65rem] text-muted-foreground">
            {shortcutLabel(isMacPlatform(navigator))}
          </kbd>
        </button>
      </div>

      <nav className="flex-1 overflow-y-auto px-3 py-2">
        {NAV_GROUPS.map((group) => (
          <div key={group.label} className="mb-5">
            <p className="px-2 pb-1.5 font-mono text-[0.65rem] uppercase tracking-[0.2em] text-muted-foreground">
              {group.label}
            </p>
            <ul className="flex flex-col gap-0.5">
              {group.items.map((item) => {
                const badge = navBadge(item, attention, inbox, queue.data)
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
                              aria-label={badge.title}
                              className={cn(
                                'inline-flex h-4 min-w-4 items-center justify-center rounded-full border px-1 font-mono text-[0.65rem]',
                                badge.tone === 'active'
                                  ? 'gap-1 border-teal/50 bg-teal/12 text-teal'
                                  : badge.tone === 'warn'
                                    ? 'border-warn/50 bg-warn/12 text-warn'
                                    : 'border-border bg-muted/60 text-muted-foreground',
                              )}
                            >
                              {badge.tone === 'active' && (
                                <span
                                  aria-hidden="true"
                                  className="size-1 rounded-full bg-current"
                                />
                              )}
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
          <dd className="min-w-0">
            <Link
              to="/settings"
              hash="updates"
              title={updateHint(update.data) ?? cliLabel ?? 'unavailable'}
              className="inline-flex max-w-full items-center gap-1.5 text-foreground/75 transition-colors hover:text-foreground"
            >
              <span className="truncate">{cliLabel ?? '—'}</span>
              {update.data?.channel === 'dev' && (
                <span className="shrink-0 rounded-sm border border-teal/40 px-1 text-[0.6rem] uppercase tracking-[0.1em] text-teal">
                  dev
                </span>
              )}
              {needsAttention(update.data) && (
                <>
                  <span
                    aria-hidden="true"
                    className="size-1.5 shrink-0 rounded-full bg-warn"
                  />
                  <span className="sr-only">(update waiting)</span>
                </>
              )}
            </Link>
          </dd>
        </dl>
      </div>
    </aside>
  )
}
