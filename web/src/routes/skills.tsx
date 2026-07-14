import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute, Link } from '@tanstack/react-router'
import {
  Download,
  ExternalLink,
  Pin,
  PinOff,
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
  TerminalCard,
  useActiveRepo,
} from '@/components/trau'
import { cn } from '@/lib/utils'
import { reposQueryOptions } from '@/lib/runs'
import { writeConfig } from '@/lib/config'
import { recentEventsQueryOptions } from '@/lib/events'
import {
  installSkill,
  latestNoSkillsTicket,
  removeSkill,
  skillPageUrl,
  skillsQueryOptions,
  skillsSearchQueryOptions,
  toggleRequired,
  type InstalledSkill,
  type RecommendedSkill,
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
          Manage the agent skills installed for this repo. Pin the ones the build
          agent must always load, and pull new ones from the{' '}
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
  const toggleRequiredMut = useMutation({
    mutationFn: (name: string) =>
      writeConfig(repo, {
        key: 'REQUIRED_SKILLS',
        value: toggleRequired(required, name),
        layer: 'project',
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: skillsQueryOptions(repo).queryKey })
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

  const requiredSet = new Set(required)
  const busy = install.isPending || remove.isPending

  return (
    <div className="flex flex-col gap-6">
      <SkillHealthBanner repo={repo} />

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

      <InstalledList
        skills={data.installed}
        requiredSet={requiredSet}
        onToggleRequired={(name) => toggleRequiredMut.mutate(name)}
        toggling={toggleRequiredMut.isPending}
        onRemove={(skill) => {
          setActionError(null)
          setRemoveTarget(skill)
        }}
        busy={busy}
      />

      <ConfirmDialog
        open={installTarget !== null}
        onOpenChange={(open) => !open && setInstallTarget(null)}
        windowTitle="install skill"
        title={`Install ${installTarget?.name}?`}
        description={
          <>
            Runs <code className="text-foreground">skills add {installTarget?.pkg}</code>{' '}
            in {repo}
            {installTarget?.source ? ` from ${installTarget.source}` : ''}. The
            skill becomes available to every agent phase.
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
            this repo and drops its pin. Agents can no longer load it.
          </>
        }
        confirmLabel="Remove"
        destructive
        onConfirm={() => removeTarget && remove.mutate(removeTarget.name)}
      />
    </div>
  )
}

function SkillHealthBanner({ repo }: { repo: string }) {
  const { data } = useQuery(recentEventsQueryOptions(repo))
  const ticket = useMemo(
    () => latestNoSkillsTicket(data?.events ?? []),
    [data],
  )
  if (!ticket) return null

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
        <p className="font-mono text-sm font-medium text-warn">
          A recent build loaded no skills
        </p>
        <p className="font-sans text-sm leading-relaxed text-muted-foreground">
          {ticket}&apos;s build ran without loading any skill. Review whether the
          right ones are installed and pinned —{' '}
          <Link
            to="/runs/$repo/$ticket"
            params={{ repo, ticket }}
            className="text-warn underline-offset-4 hover:underline"
          >
            open the run
          </Link>
          .
        </p>
      </div>
    </div>
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

function InstalledList({
  skills,
  requiredSet,
  onToggleRequired,
  toggling,
  onRemove,
  busy,
}: {
  skills: InstalledSkill[]
  requiredSet: Set<string>
  onToggleRequired: (name: string) => void
  toggling: boolean
  onRemove: (skill: InstalledSkill) => void
  busy: boolean
}) {
  return (
    <TerminalCard
      title={`installed · ${skills.length}`}
      bodyClassName="p-0"
    >
      {skills.length === 0 ? (
        <p className="px-4 py-6 font-mono text-sm text-muted-foreground">
          No skills installed. Pull one from the registry above.
        </p>
      ) : (
        <div className="divide-y divide-border/60">
          {skills.map((skill) => (
            <InstalledRow
              key={skill.name}
              skill={skill}
              required={requiredSet.has(skill.name)}
              onToggleRequired={() => onToggleRequired(skill.name)}
              toggling={toggling}
              onRemove={() => onRemove(skill)}
              busy={busy}
            />
          ))}
        </div>
      )}
    </TerminalCard>
  )
}

function InstalledRow({
  skill,
  required,
  onToggleRequired,
  toggling,
  onRemove,
  busy,
}: {
  skill: InstalledSkill
  required: boolean
  onToggleRequired: () => void
  toggling: boolean
  onRemove: () => void
  busy: boolean
}) {
  const page = skillPageUrl(skill.source)

  return (
    <div
      className={cn(
        'flex flex-wrap items-center gap-x-3 gap-y-1.5 px-4 py-3',
        required && 'border-l-2 border-primary',
      )}
    >
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <div className="flex items-center gap-2">
          <span className="font-mono text-sm text-foreground">{skill.name}</span>
          {required && (
            <span className="inline-flex items-center gap-1 rounded border border-primary/50 bg-primary/12 px-1.5 py-0.5 font-mono text-[0.65rem] text-primary">
              <Pin className="size-3" aria-hidden="true" />
              required
            </span>
          )}
        </div>
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
        className={cn(
          'font-mono',
          required
            ? 'text-primary hover:text-primary'
            : 'text-muted-foreground hover:text-foreground',
        )}
        onClick={onToggleRequired}
        disabled={toggling}
        aria-pressed={required}
        aria-label={required ? `Unpin ${skill.name}` : `Require ${skill.name}`}
      >
        {required ? (
          <PinOff className="size-3.5" aria-hidden="true" />
        ) : (
          <Pin className="size-3.5" aria-hidden="true" />
        )}
        {required ? 'Unpin' : 'Require'}
      </Button>

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
