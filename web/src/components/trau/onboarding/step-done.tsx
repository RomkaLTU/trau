import { useEffect } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { List, Play } from 'lucide-react'

import { useActiveRepo } from '@/components/trau/active-repo'
import { Button } from '@/components/ui/button'
import { type SyncResponse } from '@/lib/instances'
import {
  essentialsConfigWrites,
  trackerConfigWrites,
  type EssentialsFields,
  type TrackerFields,
  type TrackerProvider,
} from '@/lib/onboarding'

export function StepDone({
  repo,
  provider,
  trackerFields,
  essentials,
  syncResult,
}: {
  repo: string
  provider: TrackerProvider
  trackerFields: TrackerFields
  essentials: EssentialsFields
  syncResult: SyncResponse | null
}) {
  const { setScope } = useActiveRepo()
  const queryClient = useQueryClient()

  useEffect(() => {
    setScope(repo)
    void queryClient.invalidateQueries({ queryKey: ['repos'] })
    void queryClient.invalidateQueries({ queryKey: ['instances'] })
  }, [repo, setScope, queryClient])

  const writtenKeys = [
    ...trackerConfigWrites(provider, trackerFields),
    ...essentialsConfigWrites(essentials),
  ].map((w) => w.key)

  const backlog =
    provider === 'internal'
      ? 'internal store'
      : syncResult
        ? `${syncResult.issues} issues · ${syncResult.comments} comments`
        : '—'

  const summary: { label: string; value: string }[] = [
    { label: 'repo', value: repo },
    {
      label: 'tracker',
      value: trackerFields.binding ? `${provider} · ${trackerFields.binding}` : provider,
    },
    { label: 'base branch', value: essentials.baseBranch },
    { label: 'backlog', value: backlog },
  ]

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col items-center gap-3 rounded-lg border border-done/30 bg-done/5 px-6 py-8 text-center">
        <span
          aria-hidden="true"
          className="flex size-10 items-center justify-center rounded-full border border-done/50 bg-done/10 font-mono text-lg text-done"
        >
          ✓
        </span>
        <div className="flex flex-col gap-1">
          <h2 className="font-mono text-lg text-foreground">{repo} is ready</h2>
          <p className="max-w-sm text-pretty font-sans text-sm leading-relaxed text-muted-foreground">
            The tracker is wired up and the backlog is seeded. Start the loop, or browse what came
            in first.
          </p>
        </div>
        <div className="flex flex-wrap items-center justify-center gap-3">
          <Button asChild>
            <Link to="/backlog">
              <List className="size-4" />
              Open Backlog
            </Link>
          </Button>
          <Button asChild variant="outline">
            <Link to="/loop">
              <Play className="size-4" />
              Start first run
            </Link>
          </Button>
        </div>
      </div>

      <dl className="flex flex-col gap-2">
        {summary.map((row) => (
          <div key={row.label} className="flex items-baseline gap-3">
            <dt className="w-28 shrink-0 font-mono text-[0.65rem] uppercase tracking-[0.15em] text-muted-foreground">
              {row.label}
            </dt>
            <dd className="font-mono text-sm text-foreground">{row.value}</dd>
          </div>
        ))}
      </dl>

      <p className="font-sans text-xs leading-relaxed text-muted-foreground">
        Wrote{' '}
        <span className="font-mono text-foreground">{writtenKeys.join(', ')}</span> to the project
        layer (<span className="font-mono">.trau.ini</span>). Adjust anything under{' '}
        <a href="/settings" className="text-primary hover:underline">
          Settings
        </a>
        .
      </p>
    </div>
  )
}
