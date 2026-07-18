import { useEffect, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import { Check, Lock, Pencil, Search, TriangleAlert, X } from 'lucide-react'

import { Button } from '@/components/ui/button'
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
  InlineEditor,
  LayerChip,
  SecretChip,
} from '@/components/trau/settings-editor'
import { PhaseMatrix } from '@/components/trau/settings-matrix'
import { ThemeGrid } from '@/components/trau/settings-theme-grid'
import { cn } from '@/lib/utils'
import { reposQueryOptions } from '@/lib/runs'
import { configQueryOptions, type ConfigKey } from '@/lib/config'
import {
  matchesPrompt,
  promptsQueryOptions,
  repoPromptsQueryOptions,
} from '@/lib/prompts'
import {
  ROUTING_SECTION,
  THEME_SECTION,
  deriveSections,
  displayValue,
  isModified,
  matchesQuery,
  type Section,
} from '@/lib/settings'
import { standardTitle, usePageTitle } from '@/lib/page-title'

export const Route = createFileRoute('/settings')({
  component: Settings,
  loader: ({ context }) =>
    Promise.all([
      context.queryClient.ensureQueryData(reposQueryOptions),
      context.queryClient.ensureQueryData(promptsQueryOptions),
    ]),
})

function Settings() {
  usePageTitle(standardTitle('Settings'))
  const { repo: active, repos } = useActiveRepo()

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
          Layered config resolved from project → user → default. Edit any key
          and choose which layer the change writes to.
        </p>
      </header>

      {repos.length === 0 && (
        <EmptyState
          className="min-h-[300px]"
          message="No repos yet. A repo's layered config appears here once a trau loop has run in it."
        />
      )}

      {active ? <ConfigView repo={active} /> : <PromptsSection />}
    </div>
  )
}

function ConfigView({ repo }: { repo: string }) {
  const { data, error, isPending, refetch } = useQuery(configQueryOptions(repo))
  const promptsData = useQuery(promptsQueryOptions).data
  const repoPromptsData = useQuery(repoPromptsQueryOptions(repo)).data
  const [search, setSearch] = useState('')
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const [editingKey, setEditingKey] = useState<string | null>(null)
  const [savedMsg, setSavedMsg] = useState<string | null>(null)

  useEffect(() => {
    if (!savedMsg) return
    const timer = setTimeout(() => setSavedMsg(null), 3500)
    return () => clearTimeout(timer)
  }, [savedMsg])

  const keys = data?.keys ?? []
  const layers = data?.layers ?? ['project', 'user']

  const sections = useMemo(() => deriveSections(keys), [keys])
  const globalPrompts = promptsData?.prompts ?? []
  const repoPrompts = repoPromptsData?.prompts ?? []

  const query = search.trim().toLowerCase()
  const searching = query.length > 0
  const matchCount = useMemo(
    () => (searching ? keys.filter((k) => matchesQuery(k, query)).length : 0),
    [keys, query, searching],
  )
  const promptMatches =
    !searching ||
    [...globalPrompts, ...repoPrompts].some((p) => matchesPrompt(p, query))

  const navSections = useMemo(
    () => [
      ...sections.map((s) => ({
        id: s.id,
        title: s.group,
        count: s.keys.length,
        modified: s.modified,
      })),
      {
        id: 'prompts',
        title: 'Prompts',
        count: globalPrompts.length,
        modified: globalPrompts.some((p) => p.override !== null),
      },
      {
        id: 'repo-prompts',
        title: 'Repo prompts',
        count: repoPrompts.length,
        modified: repoPrompts.some((p) => p.repo_override !== null),
      },
    ],
    [sections, globalPrompts, repoPrompts],
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

  const rowFor = (item: ConfigKey, section: Section) => (
    <KeyRow
      key={item.key}
      repo={repo}
      item={item}
      layers={layers}
      hubRestart={section.hubRestart}
      editing={editingKey === item.key}
      onEdit={() => setEditingKey(item.key)}
      onCancel={() => setEditingKey(null)}
      onSaved={(target, unset) => handleSaved(item.key, target, unset)}
    />
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

    if (section.group === THEME_SECTION) {
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
      if (matched.length === 0) return null
      return (
        <section key={section.id} id={section.id} className="scroll-mt-6">
          <TerminalCard title={section.group} bodyClassName="p-0">
            <div className="flex flex-col">
              <SectionDescription section={section} />
              {matched.map((item) => rowFor(item, section))}
            </div>
          </TerminalCard>
        </section>
      )
    }

    const isExpanded = Boolean(expanded[section.id])
    const advancedCount = section.advancedKeys.length

    return (
      <section key={section.id} id={section.id} className="scroll-mt-6">
        <TerminalCard title={section.group} bodyClassName="p-0">
          <div className="flex flex-col">
            <SectionDescription section={section} />
            {section.primaryKeys.map((item) => rowFor(item, section))}
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
      <div className="flex flex-wrap items-center gap-3">
        <div className="relative w-full max-w-sm">
          <Search
            className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-faint"
            aria-hidden="true"
          />
          <input
            type="search"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="search keys and descriptions"
            aria-label="Search config keys"
            autoComplete="off"
            spellCheck={false}
            className="w-full rounded-md border border-border bg-input py-1.5 pl-8 pr-8 font-mono text-xs text-foreground placeholder:text-faint focus-visible:border-ring focus-visible:outline-none"
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
          {visibleSections.length === 0 && !promptMatches && (
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
          <RepoPromptsSection repo={repo} query={query} />
        </div>
      </div>
    </div>
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
  editing,
  onEdit,
  onCancel,
  onSaved,
}: {
  repo: string
  item: ConfigKey
  layers: string[]
  hubRestart: boolean
  editing: boolean
  onEdit: () => void
  onCancel: () => void
  onSaved: (target: string, unset: boolean) => void
}) {
  const modified = isModified(item)
  const value = displayValue(item)
  const dimmed = value === '—' || (item.bool && item.value !== '1')

  return (
    <div
      className={cn(
        'group border-b border-border/60 px-4 py-2.5 last:border-0',
        modified && 'bg-warn/[0.04]',
        editing && 'bg-secondary/20',
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
            <span title="read-only over the web">
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
