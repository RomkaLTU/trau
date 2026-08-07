import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import { Check, Globe, Lock, Pencil, Search, TriangleAlert, X } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  EmptyState,
  Eyebrow,
  TerminalCard,
  useActiveRepo,
} from '@/components/trau'
import {
  PromptsSection,
  RepoPromptsSection,
} from '@/components/trau/prompts-panel'
import {
  ProjectDefaultsSection,
  useProjectDefaultsNav,
} from '@/components/trau/project-defaults-panel'
import { TeamSyncSection } from '@/components/trau/team-sync-panel'
import {
  InlineEditor,
  LayerChip,
  SecretChip,
  ValueWarning,
} from '@/components/trau/settings-editor'
import { PhaseMatrix } from '@/components/trau/settings-matrix'
import { TrackerAdvanced } from '@/components/trau/settings-status-map'
import { ThemeGrid } from '@/components/trau/settings-theme-grid'
import { ThemePicker } from '@/components/trau/settings-appearance'
import { cn } from '@/lib/utils'
import { reposQueryOptions } from '@/lib/runs'
import {
  configScopeQueryOptions,
  type ConfigKey,
  type ConfigScope,
} from '@/lib/config'
import {
  matchesPrompt,
  promptsQueryOptions,
  repoPromptsQueryOptions,
} from '@/lib/prompts'
import { matchesProjectDefaults } from '@/lib/projects'
import { matchesTeamSync } from '@/lib/teamsync'
import {
  APPEARANCE_SECTION,
  ROUTING_SECTION,
  THEME_KEY,
  TRACKER_SECTION,
  activeTracker,
  deriveSections,
  displayValue,
  inactiveTrackerNote,
  isModified,
  matchesQuery,
  parseSettingsSearch,
  trackerHint,
  valueWarning,
  visibleKeys,
  type Section,
} from '@/lib/settings'
import { standardTitle, usePageTitle } from '@/lib/page-title'

const LANDING_MS = 2000
const LANDING_FRAMES = 20

export const Route = createFileRoute('/settings')({
  component: Settings,
  validateSearch: parseSettingsSearch,
  loader: ({ context }) =>
    Promise.all([
      context.queryClient.ensureQueryData(reposQueryOptions),
      context.queryClient.ensureQueryData(promptsQueryOptions),
    ]),
})

function Settings() {
  usePageTitle(standardTitle('Settings'))
  const { repo: active, repos, isAll } = useActiveRepo()
  const { q } = Route.useSearch()

  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-col gap-2">
        <Eyebrow glyph="action" className="text-primary">
          CONFIGURE
        </Eyebrow>
        <h1 className="text-balance text-2xl font-semibold tracking-tight text-foreground">
          Settings
        </h1>
        <p className="text-pretty text-sm leading-relaxed text-muted-foreground">
          {isAll
            ? 'Layered config resolved from user → default. Every key you edit here is written to ~/.trau.ini.'
            : 'Layered config resolved from project → user → default. Edit any key and choose which layer the change writes to.'}
        </p>
      </header>

      {repos.length === 0 && (
        <EmptyState
          className="min-h-[300px]"
          message="No repos yet. A repo's layered config appears here once a trau loop has run in it."
        />
      )}

      {active || isAll ? (
        <ConfigView repo={active} q={q} />
      ) : (
        <PromptsSection />
      )}
    </div>
  )
}

// ConfigView is the sectioned editor for one scope. A null repo is the global
// scope: the defaults every project inherits, with the repo-scoped panels left
// out because there is no repo to own them.
export function ConfigView({ repo, q }: { repo: ConfigScope; q?: string }) {
  const isGlobal = repo === null
  const { data, error, isPending, refetch } = useQuery(
    configScopeQueryOptions(repo),
  )
  const promptsData = useQuery(promptsQueryOptions).data
  const repoPromptsData = useQuery(repoPromptsQueryOptions(repo ?? '')).data
  const [search, setSearch] = useState(q ?? '')
  const [landedKey, setLandedKey] = useState(q ?? '')
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const [editingKey, setEditingKey] = useState<string | null>(null)
  const [savedMsg, setSavedMsg] = useState<string | null>(null)

  useEffect(() => {
    if (!savedMsg) return
    const timer = setTimeout(() => setSavedMsg(null), 3500)
    return () => clearTimeout(timer)
  }, [savedMsg])

  // The rows only exist once the config lands, so the highlight window starts
  // there — on a slow query it would otherwise expire against the skeleton.
  useEffect(() => {
    if (!q || isPending) return
    setSearch(q)
    setLandedKey(q)
    const timer = setTimeout(() => setLandedKey(''), LANDING_MS)
    return () => clearTimeout(timer)
  }, [q, isPending])

  const keys = useMemo(() => visibleKeys(data?.keys ?? []), [data])
  const layers = data?.layers ?? (isGlobal ? ['user'] : ['project', 'user'])

  // No repo means no active tracker to filter by: shared credentials in
  // ~/.trau.ini are legitimate whichever tracker a project points at.
  const tracker = useMemo(() => activeTracker(keys), [keys])
  const sections = useMemo(
    () => deriveSections(keys, isGlobal ? undefined : tracker),
    [keys, isGlobal, tracker],
  )
  const globalPrompts = promptsData?.prompts ?? []
  const repoPrompts = isGlobal ? [] : (repoPromptsData?.prompts ?? [])

  const query = search.trim().toLowerCase()
  const searching = query.length > 0
  const landed = landedKey.toLowerCase()
  const matchCount = useMemo(
    () => (searching ? keys.filter((k) => matchesQuery(k, query)).length : 0),
    [keys, query, searching],
  )
  const repoDefaultsNav = useProjectDefaultsNav(repo ?? '')
  const projectDefaultsNav = isGlobal ? null : repoDefaultsNav
  const panelMatches =
    !searching ||
    [...globalPrompts, ...repoPrompts].some((p) => matchesPrompt(p, query)) ||
    (projectDefaultsNav !== null && matchesProjectDefaults(query)) ||
    (!isGlobal && matchesTeamSync(query))

  const navSections = useMemo(
    () => [
      ...(projectDefaultsNav ? [projectDefaultsNav] : []),
      ...sections
        .map((s) => ({
          id: s.id,
          title: s.group,
          count:
            s.keys.length +
            (searching
              ? s.hiddenKeys.filter((k) => matchesQuery(k, query)).length
              : 0),
          modified: s.modified,
        }))
        .filter((s) => s.count > 0),
      {
        id: 'prompts',
        title: 'Prompts',
        count: globalPrompts.length,
        modified: globalPrompts.some((p) => p.override !== null),
      },
      ...(isGlobal
        ? []
        : [
            {
              id: 'repo-prompts',
              title: 'Repo prompts',
              count: repoPrompts.length,
              modified: repoPrompts.some((p) => p.repo_override !== null),
            },
            {
              id: 'team-sync-status',
              title: 'Team sync status',
              count: 0,
              modified: false,
            },
          ]),
    ],
    [
      projectDefaultsNav,
      sections,
      globalPrompts,
      repoPrompts,
      isGlobal,
      searching,
      query,
    ],
  )

  if (isPending && !error) return <ConfigSkeleton />

  if (error) {
    return (
      <TerminalCard
        title="error"
        bodyClassName="flex flex-col items-start gap-3 p-6"
      >
        <p
          className="inline-flex items-center gap-2 font-mono text-xs text-fail"
          role="alert"
        >
          <TriangleAlert className="size-3.5" aria-hidden="true" />
          {String((error as Error).message)}
        </p>
        <Button
          variant="outline"
          size="sm"
          className="font-mono text-xs"
          onClick={() => refetch()}
        >
          retry
        </Button>
      </TerminalCard>
    )
  }

  const handleSaved = (savedKey: string, target: string, unset: boolean) => {
    setEditingKey(null)
    setSavedMsg(
      unset
        ? `${savedKey} reset (removed from ${target})`
        : `${savedKey} written to ${target} layer`,
    )
  }

  const rowFor = (item: ConfigKey, section: Section, revealed = false) => (
    <KeyRow
      key={item.key}
      repo={repo}
      item={item}
      layers={layers}
      hubRestart={section.hubRestart}
      hint={isGlobal ? null : trackerHint(item.key, tracker)}
      inactiveNote={revealed ? inactiveTrackerNote(item) : null}
      landed={landed !== '' && item.key.toLowerCase() === landed}
      editing={editingKey === item.key}
      onEdit={() => setEditingKey(item.key)}
      onCancel={() => setEditingKey(null)}
      onSaved={(target, unset) => handleSaved(item.key, target, unset)}
    />
  )

  // THEME renders as the picker wherever it shows up; a THEME the hub refuses to
  // write over the web keeps the plain read-only row.
  const themeItemIn = (section: Section, items: ConfigKey[]) =>
    section.group === APPEARANCE_SECTION
      ? items.find((k) => k.key === THEME_KEY && k.editable)
      : undefined

  const pickerFor = (item: ConfigKey, section: Section) => (
    <div className="border-b border-border/60 p-4">
      <ThemePicker
        repo={repo}
        item={item}
        layers={layers}
        hubRestart={section.hubRestart}
        onSaved={handleSaved}
      />
    </div>
  )

  const advancedBody = (section: Section) => {
    const editorProps = {
      repo,
      layers,
      hubRestart: section.hubRestart,
      editingKey,
      onEdit: setEditingKey,
      onCancel: () => setEditingKey(null),
      onSaved: handleSaved,
    }

    if (section.group === ROUTING_SECTION) {
      return (
        <div className="p-4">
          <PhaseMatrix keys={section.advancedKeys} {...editorProps} />
        </div>
      )
    }

    // The status-mapping editor reads a repo's own /tracker/status-options, so
    // outside a repo the mapping keys fall back to their generic rows.
    if (section.group === TRACKER_SECTION && repo !== null) {
      return (
        <TrackerAdvanced
          repo={repo}
          keys={section.advancedKeys}
          layers={layers}
          hubRestart={section.hubRestart}
          onSaved={handleSaved}
          renderRow={(item) => rowFor(item, section)}
        />
      )
    }

    if (section.group === APPEARANCE_SECTION) {
      const colorKeys = section.advancedKeys.filter((k) => k.kind === 'color')
      const otherKeys = section.advancedKeys.filter((k) => k.kind !== 'color')
      return (
        <>
          {otherKeys.map((item) => rowFor(item, section))}
          {colorKeys.length > 0 && (
            <div className="p-4">
              <ThemeGrid keys={colorKeys} {...editorProps} />
            </div>
          )}
        </>
      )
    }

    return section.advancedKeys.map((item) => rowFor(item, section))
  }

  const renderSection = (section: Section) => {
    if (searching) {
      const matched = section.keys.filter((k) => matchesQuery(k, query))
      const revealed = section.hiddenKeys.filter((k) => matchesQuery(k, query))
      if (matched.length === 0 && revealed.length === 0) return null
      const matchedTheme = themeItemIn(section, matched)
      return (
        <section key={section.id} id={section.id} className="scroll-mt-6">
          <TerminalCard title={section.group} bodyClassName="p-0">
            <div className="flex flex-col">
              <SectionDescription section={section} />
              {matchedTheme && pickerFor(matchedTheme, section)}
              {matched
                .filter((item) => item !== matchedTheme)
                .map((item) => rowFor(item, section))}
              {revealed.map((item) => rowFor(item, section, true))}
            </div>
          </TerminalCard>
        </section>
      )
    }

    if (section.keys.length === 0) return null

    const isExpanded = Boolean(expanded[section.id])
    const advancedCount = section.advancedKeys.length
    const themeItem = themeItemIn(section, section.primaryKeys)

    return (
      <section key={section.id} id={section.id} className="scroll-mt-6">
        <TerminalCard title={section.group} bodyClassName="p-0">
          <div className="flex flex-col">
            <SectionDescription section={section} />
            {themeItem && pickerFor(themeItem, section)}
            {section.primaryKeys
              .filter((item) => item !== themeItem)
              .map((item) => rowFor(item, section))}
            {advancedCount > 0 && (
              <>
                {isExpanded && advancedBody(section)}
                <div className={cn(isExpanded && 'border-t border-border/60')}>
                  <AdvancedExpander
                    count={advancedCount}
                    expanded={isExpanded}
                    sectionTitle={section.group}
                    onToggle={() =>
                      setExpanded((prev) => ({
                        ...prev,
                        [section.id]: !prev[section.id],
                      }))
                    }
                  />
                </div>
              </>
            )}
          </div>
        </TerminalCard>
      </section>
    )
  }

  const visibleSections = sections.map(renderSection).filter(Boolean)

  return (
    <div className="flex flex-col gap-4">
      {isGlobal && <GlobalScopeBanner />}

      <div className="flex flex-wrap items-center gap-3">
        <div className="relative w-full max-w-sm">
          <Search
            className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-faint"
            aria-hidden="true"
          />
          <Input
            type="search"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="search keys and descriptions"
            aria-label="Search config keys"
            autoComplete="off"
            spellCheck={false}
            className="h-auto py-1.5 pl-8 pr-8 font-mono text-xs placeholder:text-faint"
          />
          {searching && (
            <button
              type="button"
              onClick={() => setSearch('')}
              aria-label="Clear search"
              className="absolute right-2 top-1/2 -translate-y-1/2 rounded p-0.5 text-faint transition-colors hover:text-foreground"
            >
              <X className="size-3.5" aria-hidden="true" />
            </button>
          )}
        </div>
        {searching && (
          <span
            className="font-mono text-xs tabular-nums text-muted-foreground"
            role="status"
          >
            {matchCount} of {keys.length} keys
          </span>
        )}
        {savedMsg && (
          <span
            className="inline-flex items-center gap-1.5 rounded-md border border-done/50 bg-done/12 px-2.5 py-1 font-mono text-xs text-done"
            role="status"
          >
            <Check className="size-3.5" aria-hidden="true" />
            {savedMsg}
          </span>
        )}
      </div>

      <SectionNav sections={navSections} variant="mobile" />

      <div className="flex items-start gap-6">
        <SectionNav sections={navSections} variant="desktop" />

        <div className="flex min-w-0 flex-1 flex-col gap-4">
          {repo !== null && <ProjectDefaultsSection repo={repo} query={query} />}
          {visibleSections.length === 0 && !panelMatches && (
            <TerminalCard
              title="search"
              bodyClassName="flex flex-col items-start gap-2 p-6"
            >
              <p className="font-mono text-xs text-muted-foreground">
                no keys match “{search.trim()}”
              </p>
              <button
                type="button"
                onClick={() => setSearch('')}
                className="font-mono text-xs text-primary underline-offset-2 hover:underline"
              >
                clear search
              </button>
            </TerminalCard>
          )}
          {visibleSections}
          <PromptsSection query={query} />
          {repo !== null && (
            <>
              <RepoPromptsSection repo={repo} query={query} />
              <TeamSyncSection repo={repo} query={query} />
            </>
          )}
        </div>
      </div>
    </div>
  )
}

function GlobalScopeBanner() {
  return (
    <p
      className="inline-flex items-start gap-2 rounded-md border border-info/50 bg-info/10 px-3 py-2 text-xs leading-relaxed text-muted-foreground"
      role="status"
    >
      <Globe className="mt-0.5 size-3.5 shrink-0 text-info" aria-hidden="true" />
      <span>
        <span className="font-medium text-foreground">Global defaults</span> —
        apply to every project unless a project overrides them. Edits are written
        to <span className="font-mono">~/.trau.ini</span>.
      </span>
    </p>
  )
}

function SectionDescription({ section }: { section: Section }) {
  return (
    <p className="border-b border-border/60 px-4 py-2 text-xs leading-relaxed text-muted-foreground">
      {section.description}
      {section.hubRestart && (
        <span className="text-faint"> · applies on hub restart</span>
      )}
    </p>
  )
}

function ConfigSkeleton() {
  return (
    <div
      className="flex flex-col gap-4"
      aria-busy="true"
      aria-label="Loading settings"
    >
      {[0, 1, 2].map((i) => (
        <TerminalCard
          key={i}
          title="loading"
          bodyClassName="flex flex-col gap-3 p-4"
        >
          {[0, 1, 2, 3].map((j) => (
            <div key={j} className="flex items-center gap-3">
              <div className="h-3 w-40 animate-pulse rounded bg-secondary" />
              <div className="h-3 w-14 animate-pulse rounded bg-secondary/70" />
              <div className="ml-auto h-3 w-24 animate-pulse rounded bg-secondary/70" />
            </div>
          ))}
        </TerminalCard>
      ))}
    </div>
  )
}

function AdvancedExpander({
  count,
  expanded,
  sectionTitle,
  onToggle,
}: {
  count: number
  expanded: boolean
  sectionTitle: string
  onToggle: () => void
}) {
  return (
    <button
      type="button"
      onClick={onToggle}
      aria-expanded={expanded}
      className="flex w-full items-center gap-2 px-4 py-2 font-mono text-xs text-faint transition-colors hover:bg-secondary/40 hover:text-muted-foreground"
    >
      <span aria-hidden="true" className="tracking-[0.3em]">
        · · ·
      </span>
      {expanded ? 'hide' : ''} {count} advanced
      <span className="sr-only">keys in {sectionTitle}</span>
    </button>
  )
}

interface NavSection {
  id: string
  title: string
  count: number
  modified: boolean
}

function SectionNav({
  sections,
  variant,
}: {
  sections: NavSection[]
  variant: 'desktop' | 'mobile'
}) {
  if (variant === 'desktop') {
    return (
      <nav
        aria-label="Settings sections"
        className="sticky top-6 hidden max-h-[calc(100vh-3rem)] w-52 shrink-0 flex-col gap-0.5 self-start overflow-y-auto lg:flex"
      >
        {sections.map((s) => (
          <a
            key={s.id}
            href={`#${s.id}`}
            className="group flex items-center gap-2 rounded-md px-2.5 py-1.5 font-mono text-xs text-muted-foreground transition-colors hover:bg-secondary/60 hover:text-foreground"
          >
            <span
              aria-hidden="true"
              className={cn(
                'size-1.5 shrink-0 rounded-full',
                s.modified ? 'bg-warn' : 'bg-transparent',
              )}
            />
            <span className="min-w-0 truncate">{s.title}</span>
            <span className="ml-auto shrink-0 text-[0.65rem] text-faint tabular-nums">
              {s.count}
            </span>
            {s.modified && (
              <span className="sr-only">(contains modified keys)</span>
            )}
          </a>
        ))}
      </nav>
    )
  }

  return (
    <nav
      aria-label="Settings sections"
      className="-mx-1 flex gap-1.5 overflow-x-auto px-1 pb-2 lg:hidden"
    >
      {sections.map((s) => (
        <a
          key={s.id}
          href={`#${s.id}`}
          className="inline-flex shrink-0 items-center gap-1.5 rounded-full border border-border bg-card px-2.5 py-1 font-mono text-[0.7rem] text-muted-foreground transition-colors hover:text-foreground"
        >
          {s.modified && (
            <span
              aria-hidden="true"
              className="size-1.5 rounded-full bg-warn"
            />
          )}
          {s.title}
          <span className="text-faint tabular-nums">{s.count}</span>
        </a>
      ))}
    </nav>
  )
}

function KeyRow({
  repo,
  item,
  layers,
  hubRestart,
  hint,
  inactiveNote,
  landed,
  editing,
  onEdit,
  onCancel,
  onSaved,
}: {
  repo: ConfigScope
  item: ConfigKey
  layers: string[]
  hubRestart: boolean
  hint?: string | null
  inactiveNote?: string | null
  landed: boolean
  editing: boolean
  onEdit: () => void
  onCancel: () => void
  onSaved: (target: string, unset: boolean) => void
}) {
  const modified = isModified(item)
  const value = displayValue(item)
  const dimmed = value === '—' || (item.bool && item.value !== '1')
  const warning = valueWarning(item.key, item.value)
  const row = useRef<HTMLDivElement>(null)

  // The panels above the config load in after the row does and push it down, so
  // the aim is held for a few frames instead of fired once.
  useEffect(() => {
    if (!landed) return
    let frames = LANDING_FRAMES
    let handle = requestAnimationFrame(function aim() {
      row.current?.scrollIntoView({ block: 'center' })
      if (frames-- > 0) handle = requestAnimationFrame(aim)
    })
    return () => cancelAnimationFrame(handle)
  }, [landed])

  return (
    <div
      ref={row}
      className={cn(
        'group border-b border-border/60 px-4 py-2.5 transition-[background-color,box-shadow] duration-700 last:border-0',
        modified && 'bg-warn/[0.04]',
        editing && 'bg-secondary/20',
        landed && 'bg-primary/5 inset-ring-2 inset-ring-primary/50',
      )}
    >
      <div className="flex items-center gap-2.5">
        <span
          aria-hidden="true"
          className={cn(
            'size-1.5 shrink-0 rounded-full',
            modified ? 'bg-warn' : 'bg-transparent',
          )}
          title={modified ? 'modified from default' : undefined}
        />

        <span className="min-w-0 truncate font-mono text-xs text-foreground">
          {item.key}
        </span>

        <LayerChip layer={item.layer} />
        {item.secret && <SecretChip />}
        {inactiveNote && (
          <span className="shrink-0 rounded border border-border bg-secondary/50 px-1.5 py-0.5 font-mono text-[0.65rem] text-muted-foreground">
            {inactiveNote}
          </span>
        )}

        <span className="ml-auto flex shrink-0 items-center gap-2">
          <span
            className={cn(
              'font-mono text-xs',
              dimmed ? 'text-faint' : 'text-foreground',
            )}
          >
            {value}
          </span>

          {item.editable ? (
            <button
              type="button"
              onClick={editing ? onCancel : onEdit}
              className="rounded p-1 text-muted-foreground opacity-0 transition-opacity hover:text-foreground focus-visible:opacity-100 group-hover:opacity-100"
              aria-label={`Edit ${item.key}`}
            >
              <Pencil className="size-3.5" aria-hidden="true" />
            </button>
          ) : (
            <span title={item.disabled_reason ?? 'read-only over the web'}>
              <Lock className="size-3.5 text-faint" aria-hidden="true" />
              <span className="sr-only">{item.key} is read-only</span>
            </span>
          )}
        </span>
      </div>

      {item.description && (
        <p className="mt-1 pl-4 text-xs leading-relaxed text-muted-foreground">
          {item.description}
        </p>
      )}

      {hint && (
        <p className="mt-1 pl-4 font-mono text-[0.7rem] text-faint">{hint}</p>
      )}

      {item.disabled_reason && (
        <div className="mt-1.5 pl-4">
          <ValueWarning text={item.disabled_reason} />
        </div>
      )}

      {warning && !editing && (
        <div className="mt-1.5 pl-4">
          <ValueWarning text={warning} />
        </div>
      )}

      {editing && (
        <div className="mt-2 pl-4">
          <InlineEditor
            repo={repo}
            item={item}
            layers={layers}
            hubRestart={hubRestart}
            onCancel={onCancel}
            onSaved={onSaved}
          />
        </div>
      )}
    </div>
  )
}
