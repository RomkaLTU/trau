import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useNavigate, useRouterState } from '@tanstack/react-router'
import {
  Check,
  FolderGit2,
  GitBranch,
  History,
  ListChecks,
  Settings,
} from 'lucide-react'

import { ALL_SCOPE, useActiveRepo } from '@/components/trau/active-repo'
import { NAV_GROUPS, type NavItem } from '@/components/trau/nav-items'
import { StatusPill } from '@/components/trau/status-pill'
import {
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from '@/components/ui/command'
import { configQueryOptions } from '@/lib/config'
import { useNow } from '@/lib/elapsed'
import { instancesQueryOptions } from '@/lib/instances'
import { formatAge } from '@/lib/ledger'
import { boardPill } from '@/lib/overview'
import { matchesQuery } from '@/lib/palette-filter'
import { isPaletteShortcut, movesHighlight } from '@/lib/palette-keys'
import { loadRecents, visibleRecents, type RecentEntry } from '@/lib/recents'
import { matchRuns } from '@/lib/run-search'
import { runsQueryOptions, type Run } from '@/lib/runs'
import {
  globalSearchQueryOptions,
  issueSearchQueryOptions,
  type SearchResult,
} from '@/lib/search'
import { displayValue, matchSettings } from '@/lib/settings'
import { suggestFor, type SuggestionEntry } from '@/lib/suggestions'

const GROUP_HEADING =
  '[&_[cmdk-group-heading]]:font-mono [&_[cmdk-group-heading]]:text-[0.65rem] [&_[cmdk-group-heading]]:uppercase [&_[cmdk-group-heading]]:tracking-[0.2em] [&_[cmdk-group-heading]]:font-normal'

const NAV_ITEMS = NAV_GROUPS.flatMap((group) => group.items)

type IssueRow = SearchResult & { repo?: string }
type RunRow = Run & { repo?: string }

interface SettingRow {
  key: string
  section: string
  value: string
  repo?: string
}

// Under "All projects" the same ticket id can come from two repos, and cmdk
// selects on the value, so a cross-repo row is keyed by both.
function rowValue(kind: string, id: string, repo?: string) {
  return repo ? `${kind}:${repo}:${id}` : `${kind}:${id}`
}

function RepoChip({ repo }: { repo?: string }) {
  if (!repo) return null
  return (
    <span className="shrink-0 truncate text-[0.65rem] text-muted-foreground">
      {repo}
    </span>
  )
}

function runAge(run: Run, now: number): string {
  if (!run.updated_at) return '—'
  return formatAge(Math.max(0, now - Date.parse(run.updated_at)))
}

function recentIcon(entry: RecentEntry) {
  if (entry.kind === 'project') return GitBranch
  if (entry.kind === 'run') return ListChecks
  return NAV_ITEMS.find((item) => item.to === entry.path)?.icon ?? History
}

export function CommandPalette({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { repo, repos, isAll, setScope, autoScope, openSwitcher } =
    useActiveRepo()
  const navigate = useNavigate()
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const [query, setQuery] = useState('')
  const [debounced, setDebounced] = useState('')
  const [selected, setSelected] = useState('')
  const listRef = useRef<HTMLDivElement>(null)
  const highlightMoved = useRef(false)
  const trimmed = query.trim()

  useEffect(() => {
    const id = setTimeout(() => setDebounced(trimmed), 150)
    return () => clearTimeout(id)
  }, [trimmed])

  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if (!isPaletteShortcut(e)) return
      e.preventDefault()
      onOpenChange(!open)
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [open, onOpenChange])

  useEffect(() => {
    if (open) return
    setQuery('')
    setSelected('')
    highlightMoved.current = false
  }, [open])

  const { data: instancesData } = useQuery(instancesQueryOptions)
  const suggestions = useMemo(
    () =>
      open
        ? suggestFor({
            pathname,
            scope: repo,
            instances: instancesData?.instances ?? [],
          })
        : [],
    [open, pathname, repo, instancesData],
  )

  const recents = useMemo(
    () =>
      open
        ? visibleRecents(loadRecents(), {
            path: pathname,
            repo,
            repos: repos.map((r) => r.name),
          })
        : [],
    [open, pathname, repo, repos],
  )

  // Under one repo the palette queries that repo's paths; under "All projects"
  // a single hub-side query fans the same three groups out over every repo.
  const scopedRepo = isAll ? '' : (repo ?? '')
  const scopedSearch = scopedRepo !== '' && trimmed !== ''
  const globalSearch = isAll && trimmed !== ''
  const searching = scopedSearch || globalSearch

  // Holding the previous hits mounted while the next query flies keeps the
  // highlight on the row the user picked instead of churning it through cmdk.
  const issues = useQuery({
    ...issueSearchQueryOptions(scopedRepo, debounced),
    placeholderData: (previous) => previous,
  })
  const global = useQuery({
    ...globalSearchQueryOptions(debounced),
    enabled: open && isAll && debounced !== '',
    placeholderData: (previous) => previous,
  })
  const globalData = global.data

  const issueRows: IssueRow[] = globalSearch
    ? (globalData?.results.issues ?? [])
    : scopedSearch
      ? (issues.data?.results ?? [])
      : []
  const issuesPending =
    searching &&
    (debounced !== trimmed ||
      (globalSearch ? global.isFetching : issues.isFetching))

  const config = useQuery({
    ...configQueryOptions(scopedRepo),
    enabled: open && scopedSearch,
  })
  const settingRows = useMemo<SettingRow[]>(() => {
    if (globalSearch) {
      return (globalData?.results.settings ?? []).map((row) => ({
        key: row.key,
        section: row.group,
        value: row.value,
        repo: row.repo,
      }))
    }
    if (!scopedSearch) return []
    return matchSettings(config.data?.keys ?? [], trimmed).map(
      ({ item, section }) => ({
        key: item.key,
        section,
        value: displayValue(item),
      }),
    )
  }, [globalSearch, globalData, scopedSearch, config.data, trimmed])

  const runs = useQuery({
    ...runsQueryOptions(scopedRepo),
    enabled: open && scopedSearch,
  })
  const runRows = useMemo<RunRow[]>(() => {
    if (globalSearch) return globalData?.results.runs ?? []
    return scopedSearch ? matchRuns(runs.data?.runs ?? [], trimmed) : []
  }, [globalSearch, globalData, scopedSearch, runs.data, trimmed])
  const now = useNow(30_000)

  // cmdk only auto-selects when nothing is selected, so late-arriving issue rows
  // would leave the highlight on a static row: move it to the top hit ourselves,
  // unless the user has already moved the highlight.
  const topIssue = issueRows[0]
  const topIssueValue = topIssue
    ? rowValue('issue', topIssue.id, topIssue.repo)
    : ''
  useEffect(() => {
    if (topIssueValue && !highlightMoved.current) setSelected(topIssueValue)
  }, [topIssueValue])

  // Rows that unmount can leave cmdk's controlled value pointing at nothing,
  // which silently kills Enter: hand the highlight back to the first row.
  useEffect(() => {
    const list = listRef.current
    if (!list || list.querySelector('[cmdk-item][aria-selected="true"]')) return
    const first = list.querySelector('[cmdk-item]')?.getAttribute('data-value')
    if (first) setSelected(first)
  })

  const projectRows = repos.filter((r) => matchesQuery(trimmed, [r.name, r.root]))
  const showAllScope = repos.length > 1 && matchesQuery(trimmed, ['All repos'])
  const showProjects = showAllScope || projectRows.length > 0
  const navRows = NAV_ITEMS.filter((item) =>
    matchesQuery(trimmed, [item.label, item.to]),
  )

  function pickScope(scope: string) {
    setScope(scope)
    onOpenChange(false)
  }

  // A gated destination under "All repos" mirrors the sidebar: auto-scope to a
  // sensible repo and follow the link, or hand off to the pulsing switcher.
  function goTo(item: NavItem) {
    onOpenChange(false)
    if (isAll && item.requiresProject && !autoScope()) {
      openSwitcher()
      return
    }
    void navigate({ to: item.to, search: item.search })
  }

  function pickSuggestion(entry: SuggestionEntry) {
    if (entry.kind === 'page') {
      goTo(entry.item)
      return
    }
    onOpenChange(false)
    void navigate({ to: entry.path })
  }

  // The run route carries its own repo, so a cross-repo hit lands without
  // moving the active scope.
  function pickIssue(result: IssueRow) {
    onOpenChange(false)
    void navigate({
      to: '/runs/$repo/$ticket',
      params: { repo: result.repo ?? scopedRepo, ticket: result.id },
    })
  }

  function pickRun(run: RunRow) {
    onOpenChange(false)
    void navigate({
      to: '/runs/$repo/$ticket',
      params: { repo: run.repo ?? scopedRepo, ticket: run.ticket },
    })
  }

  // Settings is repo-scoped, so a cross-repo key has to move the scope with it.
  function pickSetting(row: SettingRow) {
    onOpenChange(false)
    if (row.repo) setScope(row.repo)
    void navigate({ to: '/settings', search: { q: row.key } })
  }

  function pickRecent(entry: RecentEntry) {
    if (entry.kind === 'project') {
      pickScope(entry.label)
      return
    }
    if (entry.kind === 'page') {
      const item = NAV_ITEMS.find((i) => i.to === entry.path)
      if (item) {
        goTo(item)
        return
      }
    }
    onOpenChange(false)
    void navigate({ to: entry.path })
  }

  return (
    <CommandDialog
      open={open}
      onOpenChange={onOpenChange}
      className="font-mono"
      size="lg"
      shouldFilter={false}
      value={selected}
      onValueChange={setSelected}
    >
      <CommandInput
        detachSearch
        placeholder="Search issues, projects and pages…"
        value={query}
        onValueChange={setQuery}
        onKeyDown={(e) => {
          if (movesHighlight(e.nativeEvent)) highlightMoved.current = true
        }}
      />
      <CommandList ref={listRef} className="max-h-[65vh]">
        {!issuesPending && <CommandEmpty>No results.</CommandEmpty>}
        {(issueRows.length > 0 || issuesPending) && (
          <>
            <CommandGroup heading="Issues" className={GROUP_HEADING}>
              {issuesPending && (
                <p className="px-2 py-1.5 text-xs text-muted-foreground">
                  Searching…
                </p>
              )}
              {issueRows.map((result) => (
                <CommandItem
                  key={rowValue('issue', result.id, result.repo)}
                  value={rowValue('issue', result.id, result.repo)}
                  onSelect={() => pickIssue(result)}
                >
                  <span className="shrink-0 text-primary">{result.id}</span>
                  <span className="min-w-0 flex-1 truncate font-sans">
                    {result.title || 'Untitled'}
                  </span>
                  {result.status && (
                    <span className="shrink-0 text-[0.65rem] text-muted-foreground">
                      {result.status}
                    </span>
                  )}
                  {result.labels.map((label) => (
                    <span
                      key={label}
                      className="shrink-0 rounded border border-border bg-muted/60 px-1.5 text-[0.6rem] text-muted-foreground"
                    >
                      {label}
                    </span>
                  ))}
                  <RepoChip repo={result.repo} />
                </CommandItem>
              ))}
            </CommandGroup>
            {(runRows.length > 0 ||
              showProjects ||
              navRows.length > 0 ||
              settingRows.length > 0) && <CommandSeparator />}
          </>
        )}
        {runRows.length > 0 && (
          <>
            <CommandGroup heading="Runs" className={GROUP_HEADING}>
              {runRows.map((run) => {
                const pill = boardPill(run)
                return (
                  <CommandItem
                    key={rowValue('run', run.ticket, run.repo)}
                    value={rowValue('run', run.ticket, run.repo)}
                    onSelect={() => pickRun(run)}
                  >
                    <span className="shrink-0 text-primary">{run.ticket}</span>
                    <span className="min-w-0 flex-1 truncate font-sans">
                      {run.title ?? run.ticket}
                    </span>
                    <StatusPill state={pill.state} label={pill.label} />
                    <span className="shrink-0 text-[0.65rem] text-muted-foreground">
                      {runAge(run, now)}
                    </span>
                    <RepoChip repo={run.repo} />
                  </CommandItem>
                )
              })}
            </CommandGroup>
            {(showProjects || navRows.length > 0 || settingRows.length > 0) && (
              <CommandSeparator />
            )}
          </>
        )}
        {trimmed === '' && suggestions.length > 0 && (
          <>
            <CommandGroup heading="Suggested" className={GROUP_HEADING}>
              {suggestions.map((entry) => (
                <CommandItem
                  key={entry.key}
                  value={`suggest:${entry.key}`}
                  onSelect={() => pickSuggestion(entry)}
                >
                  {entry.kind === 'page' ? (
                    <>
                      <entry.item.icon />
                      <span className="flex-1 truncate">{entry.item.label}</span>
                    </>
                  ) : (
                    <>
                      {entry.kind === 'live' ? (
                        <GitBranch className="text-teal" />
                      ) : (
                        <ListChecks />
                      )}
                      <span className="flex-1 truncate">{entry.label}</span>
                      <span
                        className={
                          entry.kind === 'live'
                            ? 'text-[0.65rem] text-teal'
                            : 'text-[0.65rem] text-muted-foreground'
                        }
                      >
                        {entry.kind === 'live' ? 'live' : 'run'}
                      </span>
                    </>
                  )}
                </CommandItem>
              ))}
            </CommandGroup>
            <CommandSeparator />
          </>
        )}
        {trimmed === '' && recents.length > 0 && (
          <>
            <CommandGroup heading="Recent" className={GROUP_HEADING}>
              {recents.map((entry) => {
                const Icon = recentIcon(entry)
                return (
                  <CommandItem
                    key={entry.key}
                    value={entry.key}
                    onSelect={() => pickRecent(entry)}
                  >
                    <Icon />
                    <span className="flex-1 truncate">{entry.label}</span>
                    {entry.sublabel && (
                      <span className="truncate text-[0.65rem] text-muted-foreground">
                        {entry.sublabel}
                      </span>
                    )}
                  </CommandItem>
                )
              })}
            </CommandGroup>
            <CommandSeparator />
          </>
        )}
        {showProjects && (
          <>
            <CommandGroup heading="Projects" className={GROUP_HEADING}>
              {showAllScope && (
                <CommandItem
                  value="All repos"
                  onSelect={() => pickScope(ALL_SCOPE)}
                >
                  <FolderGit2 />
                  <span className="flex-1 truncate">All repos</span>
                  {isAll && <Check className="text-primary" />}
                </CommandItem>
              )}
              {projectRows.map((r) => (
                <CommandItem
                  key={r.root}
                  value={r.root}
                  onSelect={() => pickScope(r.name)}
                >
                  <GitBranch />
                  <span className="flex min-w-0 flex-1 flex-col">
                    <span className="truncate">{r.name}</span>
                    <span className="truncate text-[0.65rem] text-muted-foreground">
                      {r.root}
                    </span>
                  </span>
                  {!isAll && r.name === repo && (
                    <Check className="text-primary" />
                  )}
                </CommandItem>
              ))}
            </CommandGroup>
            {(navRows.length > 0 || settingRows.length > 0) && (
              <CommandSeparator />
            )}
          </>
        )}
        {navRows.length > 0 && (
          <>
            <CommandGroup heading="Navigation" className={GROUP_HEADING}>
              {navRows.map((item) => (
                <CommandItem
                  key={item.to}
                  value={item.label}
                  onSelect={() => goTo(item)}
                >
                  <item.icon />
                  <span className="flex-1 truncate">{item.label}</span>
                </CommandItem>
              ))}
            </CommandGroup>
            {settingRows.length > 0 && <CommandSeparator />}
          </>
        )}
        {settingRows.length > 0 && (
          <CommandGroup heading="Settings" className={GROUP_HEADING}>
            {settingRows.map((row) => (
              <CommandItem
                key={rowValue('setting', row.key, row.repo)}
                value={rowValue('setting', row.key, row.repo)}
                onSelect={() => pickSetting(row)}
              >
                <Settings />
                <span className="min-w-0 flex-1 truncate">{row.key}</span>
                <span className="shrink-0 text-[0.65rem] text-muted-foreground">
                  {row.section}
                </span>
                <span className="max-w-[10rem] shrink-0 truncate text-[0.65rem]">
                  {row.value}
                </span>
                <RepoChip repo={row.repo} />
              </CommandItem>
            ))}
          </CommandGroup>
        )}
      </CommandList>
    </CommandDialog>
  )
}
