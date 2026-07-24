import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute, Link } from '@tanstack/react-router'
import {
  Download,
  ExternalLink,
  Search,
  Sparkles,
  TriangleAlert,
  Trash2,
} from 'lucide-react'

import { Button } from '@/components/ui/button'
import {
  ConfirmDialog,
  EmptyState,
  Eyebrow,
  SegmentedControl,
  TerminalCard,
  useActiveRepo,
} from '@/components/trau'
import { cn } from '@/lib/utils'
import { reposQueryOptions } from '@/lib/runs'
import { writeConfig } from '@/lib/config'
import { recentEventsQueryOptions } from '@/lib/events'
import {
  autoNeverMatches,
  installSkill,
  latestNoSkillsTicket,
  loadedAgo,
  parseMatchers,
  removeSkill,
  ruleFor,
  saveSkillRules,
  scopeOf,
  skillPageUrl,
  skillsQueryOptions,
  skillsSearchQueryOptions,
  SKILL_PHASES,
  upsertRule,
  usageState,
  withoutRequired,
  type InstalledSkill,
  type RecommendedSkill,
  type SkillCoverage,
  type SkillPhase,
  type SkillPhaseCoverage,
  type SkillPlan,
  type SkillRule,
  type SkillScope,
  type SkillsResponse,
  type SkillSearchResult,
} from '@/lib/skills'
import { standardTitle, usePageTitle } from '@/lib/page-title'

export const Route = createFileRoute('/skills')({
  component: SkillsPage,
  loader: ({ context }) =>
    context.queryClient.ensureQueryData(reposQueryOptions),
})

interface InstallTarget {
  pkg: string
  name: string
  source?: string
}

const SCOPE_OPTIONS: { value: SkillScope; label: string }[] = [
  { value: 'always', label: 'Always' },
  { value: 'auto', label: 'Auto' },
  { value: 'manual', label: 'Manual' },
]

const SCOPE_TONE: Record<SkillScope, string> = {
  always: 'border-primary/50 bg-primary/12 text-primary',
  auto: 'border-info/50 bg-info/12 text-info',
  manual: 'border-border bg-secondary/40 text-muted-foreground',
}

function SkillsPage() {
  usePageTitle(standardTitle('Skills'))
  const { repo: active, repos } = useActiveRepo()

  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-col gap-2">
        <Eyebrow glyph="action" className="text-primary">
          SKILLS
        </Eyebrow>
        <h1 className="text-balance text-2xl font-semibold tracking-tight text-foreground">
          Skills
        </h1>
        <p className="text-pretty text-sm leading-relaxed text-muted-foreground">
          What this repo has installed, when each skill activates, and what runs
          actually loaded. Pull new ones from the{' '}
          <a
            href="https://skills.sh"
            target="_blank"
            rel="noreferrer"
            className="text-primary underline-offset-4 hover:underline"
          >
            skills.sh
          </a>{' '}
          registry.
        </p>
      </header>

      {repos.length === 0 && (
        <EmptyState
          className="min-h-[300px]"
          message="No repos yet. A repo's skills appear here once a trau loop has run in it."
        />
      )}

      {active && <SkillsPanel repo={active} />}
    </div>
  )
}

function SkillsPanel({ repo }: { repo: string }) {
  const { data, error, isPending } = useQuery(skillsQueryOptions(repo))
  const queryClient = useQueryClient()

  const [installTarget, setInstallTarget] = useState<InstallTarget | null>(null)
  const [removeTarget, setRemoveTarget] = useState<InstalledSkill | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)

  const onSnapshot = (snapshot: SkillsResponse) => {
    queryClient.setQueryData(skillsQueryOptions(repo).queryKey, snapshot)
  }

  const install = useMutation({
    mutationFn: (pkg: string) => installSkill(repo, pkg),
    onSuccess: (snapshot) => {
      onSnapshot(snapshot)
      setInstallTarget(null)
      setActionError(null)
    },
    onError: (err) => {
      setInstallTarget(null)
      setActionError((err as Error).message)
    },
  })

  const remove = useMutation({
    mutationFn: (name: string) => removeSkill(repo, name),
    onSuccess: (snapshot) => {
      onSnapshot(snapshot)
      setRemoveTarget(null)
      setActionError(null)
    },
    onError: (err) => {
      setRemoveTarget(null)
      setActionError((err as Error).message)
    },
  })

  const required = data?.required ?? []
  const rules = data?.rules ?? []

  // Editing a skill's activation supersedes whatever REQUIRED_SKILLS pinned it
  // to, so the pin is dropped in the same gesture that writes the rule —
  // otherwise the preview would keep naming a skill the editor says is Manual.
  const saveRules = useMutation({
    mutationFn: async ({ next, unpin }: { next: SkillRule[]; unpin: boolean }) => {
      if (unpin) {
        await writeConfig(repo, {
          key: 'REQUIRED_SKILLS',
          value: withoutRequired(required, next[0]?.skill ?? ''),
          layer: 'project',
        })
      }
      return saveSkillRules(repo, next)
    },
    onSuccess: (snapshot) => {
      onSnapshot(snapshot)
      queryClient.invalidateQueries({ queryKey: ['config', repo] })
      setActionError(null)
    },
    onError: (err) => setActionError((err as Error).message),
  })

  if (isPending && !error) {
    return <p className="font-mono text-sm text-muted-foreground">Loading…</p>
  }
  if (error) {
    return <p className="font-mono text-sm text-destructive">{String(error)}</p>
  }
  if (!data) return null

  const busy = install.isPending || remove.isPending

  const applyRule = (rule: SkillRule) =>
    saveRules.mutate({
      next: [rule, ...upsertRule(rules, rule).filter((r) => r.skill !== rule.skill)],
      unpin: required.includes(rule.skill),
    })

  return (
    <div className="flex flex-col gap-6">
      <NoSkillsWarning repo={repo} />
      <RulesProblem error={data.rules_error} unknown={data.unknown ?? []} />

      {actionError && (
        <p className="font-mono text-sm text-fail">{actionError}</p>
      )}

      <RegistrySearch
        repo={repo}
        onInstall={(t) => {
          setActionError(null)
          setInstallTarget(t)
        }}
        installing={install.isPending}
      />

      <RecommendedStrip
        recommended={data.recommended}
        projectType={data.project_type}
        onInstall={(t) => {
          setActionError(null)
          setInstallTarget(t)
        }}
      />

      <InventorySection
        skills={data.installed}
        rules={rules}
        required={required}
        onRemove={(skill) => {
          setActionError(null)
          setRemoveTarget(skill)
        }}
        busy={busy}
      />

      <ActivationSection
        skills={data.installed}
        rules={rules}
        required={required}
        plan={data.plan}
        onApply={applyRule}
        saving={saveRules.isPending}
      />

      <CoverageSection skills={data.installed} coverage={data.coverage} />

      <ConfirmDialog
        open={installTarget !== null}
        onOpenChange={(open) => !open && setInstallTarget(null)}
        windowTitle="install skill"
        title={`Install ${installTarget?.name}?`}
        description={
          <>
            Runs <code className="text-foreground">skills add {installTarget?.pkg}</code>{' '}
            in {repo}
            {installTarget?.source ? ` from ${installTarget.source}` : ''}. Set
            when it activates once it lands.
          </>
        }
        confirmLabel="Install"
        onConfirm={() => installTarget && install.mutate(installTarget.pkg)}
      />

      <ConfirmDialog
        open={removeTarget !== null}
        onOpenChange={(open) => !open && setRemoveTarget(null)}
        windowTitle="remove skill"
        title={`Remove ${removeTarget?.name}?`}
        description={
          <>
            Deletes {removeTarget?.name}
            {removeTarget?.source ? ` (from ${removeTarget.source})` : ''} from
            this repo. Agents can no longer load it.
          </>
        }
        confirmLabel="Remove"
        destructive
        onConfirm={() => removeTarget && remove.mutate(removeTarget.name)}
      />
    </div>
  )
}

function WarnBanner({
  title,
  children,
}: {
  title: string
  children: React.ReactNode
}) {
  return (
    <div
      role="status"
      className="flex items-start gap-2.5 rounded-lg border border-warn/50 bg-warn/12 px-4 py-3"
    >
      <TriangleAlert
        className="mt-0.5 size-4 shrink-0 text-warn"
        aria-hidden="true"
      />
      <div className="flex flex-col gap-0.5">
        <p className="font-mono text-sm font-medium text-warn">{title}</p>
        <p className="font-sans text-sm leading-relaxed text-muted-foreground">
          {children}
        </p>
      </div>
    </div>
  )
}

function NoSkillsWarning({ repo }: { repo: string }) {
  const { data } = useQuery(recentEventsQueryOptions(repo))
  const ticket = useMemo(
    () => latestNoSkillsTicket(data?.events ?? []),
    [data],
  )
  if (!ticket) return null

  return (
    <WarnBanner title="A recent build loaded none of its planned skills">
      {ticket}&apos;s build was told which skills to load and used none of them
      —{' '}
      <Link
        to="/runs/$repo/$ticket"
        params={{ repo, ticket }}
        className="text-warn underline-offset-4 hover:underline"
      >
        open the run
      </Link>
      .
    </WarnBanner>
  )
}

function RulesProblem({
  error,
  unknown,
}: {
  error?: string
  unknown: string[]
}) {
  if (error) {
    return (
      <WarnBanner title="Activation rules could not be read">
        <code className="text-foreground">.trau/skills-rules.json</code> failed
        to parse ({error}), so every phase is falling back to the chain until it
        is fixed.
      </WarnBanner>
    )
  }
  if (unknown.length === 0) return null
  return (
    <WarnBanner title="Rules name skills this repo cannot load">
      {unknown.join(', ')} — install them or drop the rule; they are skipped in
      the meantime.
    </WarnBanner>
  )
}

function RegistrySearch({
  repo,
  onInstall,
  installing,
}: {
  repo: string
  onInstall: (t: InstallTarget) => void
  installing: boolean
}) {
  const [input, setInput] = useState('')
  const [query, setQuery] = useState('')

  useEffect(() => {
    const t = setTimeout(() => setQuery(input.trim()), 300)
    return () => clearTimeout(t)
  }, [input])

  const { data, isFetching } = useQuery(skillsSearchQueryOptions(repo, query))
  const results = data?.results ?? []
  const unavailable = Boolean(data?.unavailable)

  return (
    <TerminalCard title="skills.sh registry" bodyClassName="p-0">
      <div className="flex flex-col gap-4 p-4">
        <div className="relative">
          <Search
            className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground"
            aria-hidden="true"
          />
          <input
            type="search"
            value={input}
            onChange={(e) => setInput(e.target.value)}
            placeholder="search the registry…"
            aria-label="Search the skills registry"
            autoComplete="off"
            spellCheck={false}
            className="w-full rounded-md border border-border bg-input py-1.5 pl-8 pr-2.5 font-mono text-sm text-foreground placeholder:text-faint focus-visible:border-ring focus-visible:outline-none"
          />
        </div>

        {query !== '' && unavailable && <ManualInstall onInstall={onInstall} />}

        {query !== '' && !unavailable && (
          <div className="flex flex-col divide-y divide-border/60">
            {isFetching && results.length === 0 && (
              <p className="py-2 font-mono text-sm text-muted-foreground">
                Searching…
              </p>
            )}
            {!isFetching && results.length === 0 && (
              <p className="py-2 font-mono text-sm text-muted-foreground">
                No skills match “{query}”.
              </p>
            )}
            {results.map((r) => (
              <SearchResultRow
                key={r.id}
                result={r}
                onInstall={onInstall}
                disabled={installing}
              />
            ))}
          </div>
        )}
      </div>
    </TerminalCard>
  )
}

function SearchResultRow({
  result,
  onInstall,
  disabled,
}: {
  result: SkillSearchResult
  onInstall: (t: InstallTarget) => void
  disabled: boolean
}) {
  const pkg = result.skill_id
    ? `${result.source}@${result.skill_id}`
    : result.source
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 py-2.5">
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <a
          href={result.url}
          target="_blank"
          rel="noreferrer"
          className="inline-flex items-center gap-1.5 font-mono text-sm text-foreground hover:text-primary"
        >
          {result.name}
          <ExternalLink className="size-3 text-muted-foreground" aria-hidden="true" />
        </a>
        <span className="font-mono text-xs text-muted-foreground">
          {result.source}
          <span className="mx-1.5 text-faint">·</span>
          {result.installs.toLocaleString()} installs
        </span>
      </div>
      <Button
        size="sm"
        variant="outline"
        className="font-mono"
        disabled={disabled}
        onClick={() =>
          onInstall({ pkg, name: result.name, source: result.source })
        }
      >
        <Download className="size-3.5" aria-hidden="true" />
        Install
      </Button>
    </div>
  )
}

function ManualInstall({
  onInstall,
}: {
  onInstall: (t: InstallTarget) => void
}) {
  const [value, setValue] = useState('')
  const spec = value.trim()

  return (
    <div className="flex flex-col gap-2 rounded-md border border-warn/40 bg-warn/8 p-3">
      <div className="flex items-start gap-2">
        <TriangleAlert
          className="mt-0.5 size-3.5 shrink-0 text-warn"
          aria-hidden="true"
        />
        <p className="font-sans text-sm leading-relaxed text-muted-foreground">
          The registry is unavailable. Install directly by source instead —{' '}
          <code className="text-foreground">owner/repo</code> or{' '}
          <code className="text-foreground">owner/repo@skill</code>.
        </p>
      </div>
      <form
        className="flex flex-wrap items-center gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          if (spec) onInstall({ pkg: spec, name: spec, source: spec })
        }}
      >
        <input
          type="text"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          placeholder="owner/repo@skill"
          aria-label="Install skill by source"
          autoComplete="off"
          spellCheck={false}
          className="min-w-0 flex-1 rounded-md border border-border bg-input px-2.5 py-1.5 font-mono text-sm text-foreground placeholder:text-faint focus-visible:border-ring focus-visible:outline-none"
        />
        <Button
          type="submit"
          size="sm"
          variant="outline"
          className="font-mono"
          disabled={spec === ''}
        >
          <Download className="size-3.5" aria-hidden="true" />
          Install
        </Button>
      </form>
    </div>
  )
}

function RecommendedStrip({
  recommended,
  projectType,
  onInstall,
}: {
  recommended: RecommendedSkill[]
  projectType: string
  onInstall: (t: InstallTarget) => void
}) {
  if (recommended.length === 0) return null

  return (
    <TerminalCard title="recommended starters">
      <div className="flex flex-col gap-3">
        <p className="font-sans text-sm leading-relaxed text-muted-foreground">
          Curated starters for {projectType ? `this ${projectType} project` : 'this project'}, not yet installed.
        </p>
        <div className="flex flex-wrap gap-2">
          {recommended.map((rec) => (
            <div
              key={rec.name}
              className="flex items-center gap-2 rounded-md border border-border bg-secondary/30 py-1 pl-3 pr-1"
            >
              <Sparkles className="size-3.5 text-primary" aria-hidden="true" />
              <a
                href={rec.url}
                target="_blank"
                rel="noreferrer"
                className="font-mono text-sm text-foreground hover:text-primary"
              >
                {rec.name}
              </a>
              <Button
                size="sm"
                variant="ghost"
                className="h-7 font-mono text-muted-foreground hover:text-foreground"
                onClick={() =>
                  onInstall({
                    pkg: rec.package,
                    name: rec.name,
                    source: rec.package.split('@')[0],
                  })
                }
                aria-label={`Install ${rec.name}`}
              >
                <Download className="size-3.5" aria-hidden="true" />
                Install
              </Button>
            </div>
          ))}
        </div>
      </div>
    </TerminalCard>
  )
}

function ScopeBadge({ scope }: { scope: SkillScope }) {
  return (
    <span
      className={cn(
        'inline-flex items-center rounded border px-1.5 py-0.5 font-mono text-[0.65rem] capitalize',
        SCOPE_TONE[scope],
      )}
    >
      {scope}
    </span>
  )
}

function InventorySection({
  skills,
  rules,
  required,
  onRemove,
  busy,
}: {
  skills: InstalledSkill[]
  rules: SkillRule[]
  required: string[]
  onRemove: (skill: InstalledSkill) => void
  busy: boolean
}) {
  return (
    <TerminalCard title={`inventory · ${skills.length}`} bodyClassName="p-0">
      {skills.length === 0 ? (
        <p className="px-4 py-6 font-mono text-sm text-muted-foreground">
          No skills installed. Pull one from the registry above.
        </p>
      ) : (
        <div className="divide-y divide-border/60">
          {skills.map((skill) => (
            <InventoryRow
              key={skill.name}
              skill={skill}
              scope={scopeOf(skill.name, rules, required)}
              onRemove={() => onRemove(skill)}
              busy={busy}
            />
          ))}
        </div>
      )}
    </TerminalCard>
  )
}

function InventoryRow({
  skill,
  scope,
  onRemove,
  busy,
}: {
  skill: InstalledSkill
  scope: SkillScope
  onRemove: () => void
  busy: boolean
}) {
  const page = skillPageUrl(skill.source)

  return (
    <div
      className={cn(
        'flex flex-wrap items-start gap-x-3 gap-y-1.5 px-4 py-3',
        skill.invalid && 'border-l-2 border-fail',
      )}
    >
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-mono text-sm text-foreground">{skill.name}</span>
          <ScopeBadge scope={scope} />
          {skill.invalid && (
            <span className="inline-flex items-center gap-1 rounded border border-fail/50 bg-fail/12 px-1.5 py-0.5 font-mono text-[0.65rem] text-fail">
              <TriangleAlert className="size-3" aria-hidden="true" />
              invalid SKILL.md
            </span>
          )}
        </div>
        {skill.invalid ? (
          <p className="font-sans text-xs leading-relaxed text-muted-foreground">
            No readable frontmatter — agents can still be told to load it, but
            it describes nothing and cannot suggest keywords.
          </p>
        ) : (
          skill.description && (
            <p className="font-sans text-xs leading-relaxed text-muted-foreground">
              {skill.description}
            </p>
          )
        )}
        {skill.source ? (
          page ? (
            <a
              href={page}
              target="_blank"
              rel="noreferrer"
              className="inline-flex w-fit items-center gap-1 font-mono text-xs text-muted-foreground hover:text-primary"
            >
              {skill.source}
              <ExternalLink className="size-3" aria-hidden="true" />
            </a>
          ) : (
            <span className="font-mono text-xs text-muted-foreground">
              {skill.source}
            </span>
          )
        ) : (
          <span className="font-mono text-xs text-faint">local · no source</span>
        )}
      </div>

      <Button
        size="sm"
        variant="ghost"
        className="font-mono text-muted-foreground hover:text-fail"
        onClick={onRemove}
        disabled={busy}
        aria-label={`Remove ${skill.name}`}
      >
        <Trash2 className="size-3.5" aria-hidden="true" />
        Remove
      </Button>
    </div>
  )
}

function ActivationSection({
  skills,
  rules,
  required,
  plan,
  onApply,
  saving,
}: {
  skills: InstalledSkill[]
  rules: SkillRule[]
  required: string[]
  plan: SkillPlan[]
  onApply: (rule: SkillRule) => void
  saving: boolean
}) {
  if (skills.length === 0) return null

  return (
    <TerminalCard title="activation policy" bodyClassName="p-0">
      <div className="flex flex-col gap-3 border-b border-border/60 px-4 py-3">
        <p className="font-sans text-sm leading-relaxed text-muted-foreground">
          <span className="text-foreground">Always</span> names a skill in every
          phase it covers, <span className="text-foreground">Auto</span> names it
          when its paths or keywords match the ticket or the slice&apos;s diff,
          and <span className="text-foreground">Manual</span> keeps it out of
          automatic sets. Rules are saved to{' '}
          <code className="text-foreground">.trau/skills-rules.json</code> and
          checked in with the repo.
        </p>
        <PlanPreview plan={plan} />
      </div>
      <div className="divide-y divide-border/60">
        {skills.map((skill) => (
          <ActivationRow
            key={skill.name}
            skill={skill}
            rule={ruleFor(rules, skill.name)}
            scope={scopeOf(skill.name, rules, required)}
            pinned={required.includes(skill.name)}
            onApply={onApply}
            saving={saving}
          />
        ))}
      </div>
    </TerminalCard>
  )
}

function PlanPreview({ plan }: { plan: SkillPlan[] }) {
  return (
    <div className="grid gap-3 sm:grid-cols-3">
      {plan.map((phase) => (
        <div
          key={phase.phase}
          className="flex flex-col gap-2 rounded-md border border-border bg-secondary/20 p-3"
        >
          <div className="flex items-baseline justify-between gap-2">
            <span className="font-mono text-xs uppercase tracking-[0.18em] text-muted-foreground">
              next {phase.phase}
            </span>
            <span className="font-mono text-[0.65rem] text-faint">
              {phase.skills.length}
            </span>
          </div>
          {phase.skills.length === 0 ? (
            <p className="font-mono text-xs text-faint">names nothing</p>
          ) : (
            <ul className="flex flex-col gap-1">
              {phase.skills.map((name) => (
                <li key={name} className="flex flex-col">
                  <span className="font-mono text-xs text-foreground">
                    {name}
                  </span>
                  <span className="font-mono text-[0.65rem] text-faint">
                    {phase.origins?.[name] ?? phase.source}
                  </span>
                </li>
              ))}
            </ul>
          )}
          {phase.fallback && (
            <p className="font-sans text-xs leading-relaxed text-muted-foreground">
              Nothing is scoped to {phase.phase} — falling back to {phase.source}.
            </p>
          )}
        </div>
      ))}
    </div>
  )
}

function ActivationRow({
  skill,
  rule,
  scope,
  pinned,
  onApply,
  saving,
}: {
  skill: InstalledSkill
  rule?: SkillRule
  scope: SkillScope
  pinned: boolean
  onApply: (rule: SkillRule) => void
  saving: boolean
}) {
  const base: SkillRule = { ...(rule ?? { skill: skill.name }), skill: skill.name, scope }
  const phases = base.phases?.length ? base.phases : [...SKILL_PHASES]
  const suggestions = (skill.suggested_keywords ?? []).filter(
    (k) => !(base.keywords ?? []).includes(k),
  )

  const apply = (patch: Partial<SkillRule>) => onApply({ ...base, ...patch })

  const togglePhase = (phase: SkillPhase) => {
    const next = phases.includes(phase)
      ? phases.filter((p) => p !== phase)
      : [...phases, phase]
    const ordered = SKILL_PHASES.filter((p) => next.includes(p))
    apply({ phases: ordered.length === SKILL_PHASES.length ? undefined : ordered })
  }

  return (
    <div className="flex flex-col gap-3 px-4 py-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">
          <span className="font-mono text-sm text-foreground">{skill.name}</span>
          {pinned && (
            <span className="font-mono text-[0.65rem] text-faint">
              pinned by REQUIRED_SKILLS
            </span>
          )}
        </div>
        <SegmentedControl
          aria-label={`Activation scope for ${skill.name}`}
          options={SCOPE_OPTIONS}
          value={scope}
          onChange={(next) => apply({ scope: next })}
        />
      </div>

      {scope !== 'manual' && (
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-mono text-xs text-muted-foreground">phases</span>
          {SKILL_PHASES.map((phase) => {
            const on = phases.includes(phase)
            return (
              <button
                key={phase}
                type="button"
                aria-pressed={on}
                disabled={saving || (on && phases.length === 1)}
                onClick={() => togglePhase(phase)}
                className={cn(
                  'rounded border px-2 py-0.5 font-mono text-xs transition-colors disabled:opacity-60',
                  on
                    ? 'border-primary/50 bg-primary/12 text-primary'
                    : 'border-border text-muted-foreground hover:text-foreground',
                )}
              >
                {phase}
              </button>
            )
          })}
        </div>
      )}

      {scope === 'auto' && (
        <div className="flex flex-col gap-2">
          <MatcherField
            label="paths"
            placeholder="web/** , **/*.go"
            values={base.paths ?? []}
            disabled={saving}
            onCommit={(paths) => apply({ paths })}
          />
          <MatcherField
            label="keywords"
            placeholder="web ui, release"
            values={base.keywords ?? []}
            disabled={saving}
            onCommit={(keywords) => apply({ keywords })}
          />
          {suggestions.length > 0 && (
            <div className="flex flex-wrap items-center gap-1.5">
              <span className="font-mono text-xs text-muted-foreground">
                suggested
              </span>
              {suggestions.map((word) => (
                <button
                  key={word}
                  type="button"
                  disabled={saving}
                  onClick={() =>
                    apply({ keywords: [...(base.keywords ?? []), word] })
                  }
                  className="rounded border border-border px-1.5 py-0.5 font-mono text-[0.65rem] text-muted-foreground transition-colors hover:border-primary/50 hover:text-primary disabled:opacity-60"
                >
                  + {word}
                </button>
              ))}
            </div>
          )}
          {autoNeverMatches(base) && (
            <p className="font-sans text-xs leading-relaxed text-muted-foreground">
              No paths and no keywords — this rule can never match, so the skill
              is never named automatically.
            </p>
          )}
        </div>
      )}
    </div>
  )
}

function MatcherField({
  label,
  placeholder,
  values,
  disabled,
  onCommit,
}: {
  label: string
  placeholder: string
  values: string[]
  disabled: boolean
  onCommit: (values: string[]) => void
}) {
  const joined = values.join(', ')
  const [draft, setDraft] = useState(joined)

  useEffect(() => setDraft(joined), [joined])

  const commit = () => {
    const next = parseMatchers(draft)
    if (next.join(', ') !== joined) onCommit(next)
  }

  return (
    <label className="flex flex-wrap items-center gap-2">
      <span className="w-16 font-mono text-xs text-muted-foreground">
        {label}
      </span>
      <input
        type="text"
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault()
            commit()
          }
        }}
        placeholder={placeholder}
        disabled={disabled}
        autoComplete="off"
        spellCheck={false}
        className="min-w-0 flex-1 rounded-md border border-border bg-input px-2.5 py-1 font-mono text-xs text-foreground placeholder:text-faint focus-visible:border-ring focus-visible:outline-none disabled:opacity-60"
      />
    </label>
  )
}

function CoverageSection({
  skills,
  coverage,
}: {
  skills: InstalledSkill[]
  coverage: SkillCoverage
}) {
  if (skills.length === 0) return null
  const silent = coverage.silent_providers ?? []

  return (
    <TerminalCard
      title={`run coverage · last ${coverage.days} days`}
      bodyClassName="p-0"
    >
      {!coverage.has_data && (
        <p className="border-b border-border/60 px-4 py-3 font-sans text-sm leading-relaxed text-muted-foreground">
          No run in the window reported which skills it loaded, so coverage reads
          as no data rather than as skills nobody uses.
          {silent.length > 0 &&
            ` ${silent.join(', ')} ${silent.length === 1 ? 'does' : 'do'} not report skill usage.`}
        </p>
      )}
      <div className="divide-y divide-border/60">
        {skills.map((skill) => (
          <CoverageRow key={skill.name} skill={skill} coverage={coverage} />
        ))}
      </div>
      <PhaseCoverage phases={coverage.phases} />
    </TerminalCard>
  )
}

function CoverageRow({
  skill,
  coverage,
}: {
  skill: InstalledSkill
  coverage: SkillCoverage
}) {
  const state = usageState(skill, coverage)

  return (
    <div
      className={cn(
        'flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-2.5',
        state === 'dead' && 'opacity-70',
      )}
    >
      <span className="min-w-0 flex-1 font-mono text-sm text-foreground">
        {skill.name}
      </span>
      {state === 'loaded' && (
        <span className="font-mono text-xs text-done">
          loaded {skill.loads}×
          <span className="mx-1.5 text-faint">·</span>
          {loadedAgo(skill.last_loaded)}
        </span>
      )}
      {state === 'dead' && (
        <span className="rounded border border-border bg-secondary/40 px-1.5 py-0.5 font-mono text-[0.65rem] text-muted-foreground">
          not loaded in {coverage.days}d
        </span>
      )}
      {state === 'unknown' && (
        <span className="font-mono text-xs text-faint">no data</span>
      )}
    </div>
  )
}

function PhaseCoverage({ phases }: { phases: SkillPhaseCoverage[] }) {
  if (phases.length === 0) return null

  return (
    <div className="border-t border-border/60">
      <p className="px-4 pt-3 font-mono text-xs uppercase tracking-[0.18em] text-muted-foreground">
        planned vs loaded
      </p>
      <div className="divide-y divide-border/60">
        {phases.map((phase) => (
          <div
            key={`${phase.ticket}-${phase.phase}-${phase.ts}`}
            className="flex flex-col gap-1.5 px-4 py-2.5"
          >
            <div className="flex flex-wrap items-baseline gap-x-2 font-mono text-xs">
              <span className="text-foreground">{phase.ticket}</span>
              <span className="text-muted-foreground">{phase.phase}</span>
              <span className="text-faint">{loadedAgo(phase.ts)}</span>
              {phase.provider && (
                <span className="text-faint">{phase.provider}</span>
              )}
            </div>
            {phase.unknown ? (
              <span className="font-mono text-xs text-faint">
                no data — this run reported no skill usage
              </span>
            ) : (
              <div className="flex flex-wrap gap-1.5">
                {phase.planned.map((name) => {
                  const loaded = phase.loaded.includes(name)
                  return (
                    <span
                      key={name}
                      className={cn(
                        'rounded border px-1.5 py-0.5 font-mono text-[0.65rem]',
                        loaded
                          ? 'border-done/50 bg-done/12 text-done'
                          : 'border-border text-faint',
                      )}
                    >
                      {name}
                    </span>
                  )
                })}
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
  )
}
