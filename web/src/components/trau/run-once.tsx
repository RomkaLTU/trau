import { useEffect, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'

import { Button } from '@/components/ui/button'
import { MakeStartableButton } from '@/components/make-startable-button'
import { useActiveRepo } from './active-repo'
import { RepoPicker } from './repo-picker'
import { TargetRepoField } from './target-repo-field'
import { TerminalCard } from './terminal-card'
import { cn } from '@/lib/utils'
import { configQueryOptions } from '@/lib/config'
import { eligibleQueryOptions } from '@/lib/eligible'
import { dryRun, startInstance } from '@/lib/instances'

const NO_OVERRIDE = 'default'

function actionError(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

export function RunOnce() {
  const navigate = useNavigate()

  const { repo: activeRepo, repos: allRepos } = useActiveRepo()
  const startable = allRepos.filter((r) => r.allowed).map((r) => r.name)
  const repo = activeRepo ?? ''
  const canRun = repo !== '' && startable.includes(repo)

  const [selected, setSelected] = useState<string | null>(null)
  const [manualId, setManualId] = useState('')
  const [provider, setProvider] = useState(NO_OVERRIDE)

  const ticket = manualId.trim() ? manualId.trim().toUpperCase() : selected

  const config = useQuery(configQueryOptions(repo))
  const providers = [NO_OVERRIDE, ...(config.data?.providers ?? [])]

  const eligible = useQuery(eligibleQueryOptions(repo))
  const tickets = eligible.data?.tickets ?? []

  const preview = useMutation({ mutationFn: () => dryRun(repo) })

  const start = useMutation({
    mutationFn: () =>
      startInstance({
        repo,
        ticket: ticket!,
        provider: provider === NO_OVERRIDE ? undefined : provider,
      }),
    onSuccess: () => {
      void navigate({
        to: '/live/$repo/$ticket',
        params: { repo, ticket: ticket! },
      })
    },
  })

  useEffect(() => {
    setSelected(null)
    setManualId('')
    setProvider(NO_OVERRIDE)
  }, [repo])

  if (!canRun)
    return (
      <NotStartableNotice
        repo={repo}
        root={allRepos.find((r) => r.name === repo)?.root}
      />
    )

  return (
    <div className="grid grid-cols-1 gap-6 lg:grid-cols-11">
      <div className="lg:col-span-6">
        <TerminalCard title="run-once">
          <form
            className="flex flex-col gap-6"
            onSubmit={(e) => {
              e.preventDefault()
              if (ticket) start.mutate()
            }}
          >
            <TargetRepoField repo={repo} />

            <div className="flex flex-col gap-1.5">
              <span className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
                eligible tickets
              </span>

              {eligible.isLoading ? (
                <ul className="flex flex-col gap-2" aria-busy="true">
                  {[0, 1, 2].map((i) => (
                    <li
                      key={i}
                      className="flex items-center gap-3 rounded-md border border-border bg-secondary/30 px-3 py-3"
                    >
                      <span className="size-3.5 shrink-0 animate-pulse rounded-full bg-muted" />
                      <span className="h-3 w-2/3 animate-pulse rounded bg-muted" />
                      <span className="ml-auto h-3 w-20 animate-pulse rounded bg-muted" />
                    </li>
                  ))}
                </ul>
              ) : eligible.error ? (
                <p className="font-mono text-sm text-destructive">
                  {actionError(eligible.error)}
                </p>
              ) : tickets.length === 0 ? (
                <div className="flex flex-col items-center justify-center gap-3 rounded-md border border-border px-6 py-10 text-center">
                  <p className="font-sans text-sm text-muted-foreground">
                    No ready tickets in {repo || 'this repo'}.
                  </p>
                  <Button asChild variant="outline" size="sm" className="font-mono">
                    <Link to="/settings">Open settings</Link>
                  </Button>
                </div>
              ) : (
                <ul className="flex flex-col gap-2">
                  {tickets.map((t) => {
                    const isSelected = !manualId.trim() && selected === t.id
                    return (
                      <li key={t.id}>
                        <button
                          type="button"
                          onClick={() => {
                            setSelected(t.id)
                            setManualId('')
                          }}
                          aria-pressed={isSelected}
                          className={cn(
                            'flex w-full items-start gap-3 rounded-md border px-3 py-3 text-left transition-colors',
                            isSelected
                              ? 'border-primary bg-primary/5'
                              : 'border-border bg-secondary/20 hover:border-ring/40 hover:bg-secondary/40',
                          )}
                        >
                          <span
                            aria-hidden="true"
                            className={cn(
                              'mt-0.5 flex size-3.5 shrink-0 items-center justify-center rounded-full border',
                              isSelected
                                ? 'border-primary'
                                : 'border-muted-foreground/50',
                            )}
                          >
                            {isSelected && (
                              <span className="size-1.5 rounded-full bg-primary" />
                            )}
                          </span>
                          <span className="flex flex-1 flex-col gap-1">
                            <span className="flex flex-wrap items-center gap-2">
                              <span className="font-mono text-sm text-primary">
                                {t.id}
                              </span>
                              <span className="font-sans text-sm text-foreground">
                                {t.title}
                              </span>
                            </span>
                            {t.labels.length > 0 && (
                              <span className="flex flex-wrap gap-1.5">
                                {t.labels.map((label) => (
                                  <span
                                    key={label}
                                    className="w-fit rounded border border-border bg-muted/60 px-1.5 py-0.5 font-mono text-[0.65rem] text-muted-foreground"
                                  >
                                    {label}
                                  </span>
                                ))}
                              </span>
                            )}
                          </span>
                        </button>
                      </li>
                    )
                  })}
                </ul>
              )}
            </div>

            <div className="flex flex-col gap-1.5">
              <label
                htmlFor="ticket-id"
                className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground"
              >
                or enter a ticket id
              </label>
              <input
                id="ticket-id"
                value={manualId}
                onChange={(e) => {
                  setManualId(e.target.value)
                  if (e.target.value) setSelected(null)
                }}
                placeholder="COD-###"
                className="w-56 rounded-md border border-border bg-input px-2.5 py-1.5 font-mono text-sm text-foreground placeholder:text-muted-foreground/60 focus-visible:border-ring focus-visible:outline-none"
              />
            </div>

            <div className="flex flex-col gap-1.5">
              <RepoPicker
                repos={providers}
                value={provider}
                onChange={setProvider}
                label="provider · this run only"
              />
              <p className="font-sans text-xs leading-relaxed text-muted-foreground">
                Reverts when the run ends.
              </p>
            </div>

            <div className="flex flex-col gap-2 border-t border-border pt-4">
              <div className="flex items-center gap-2">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="font-mono"
                  onClick={() => preview.mutate()}
                  disabled={!repo || preview.isPending}
                >
                  {preview.isPending ? 'Previewing…' : 'Preview next'}
                </Button>
                <Button
                  type="submit"
                  size="sm"
                  className="font-mono"
                  disabled={!ticket || start.isPending}
                >
                  {start.isPending ? 'Launching…' : 'Launch'}
                </Button>
              </div>
              {start.error && (
                <p className="font-mono text-xs text-destructive">
                  {actionError(start.error)}
                </p>
              )}
            </div>
          </form>
        </TerminalCard>
      </div>

      <div className="flex flex-col gap-6 lg:col-span-5">
        <TerminalCard title="dry-run" scanlines>
          {preview.isPending ? (
            <div className="flex items-center gap-2 font-mono text-sm text-muted-foreground">
              <span aria-hidden="true">○</span>
              <span>planning a pick…</span>
            </div>
          ) : preview.error ? (
            <p className="font-mono text-sm text-destructive">
              {actionError(preview.error)}
            </p>
          ) : preview.data ? (
            preview.data.ticket ? (
              <div className="flex flex-col gap-2 font-mono text-sm">
                <p className="text-teal">
                  <span aria-hidden="true">▸</span> next pick →{' '}
                  <span className="text-primary">{preview.data.ticket}</span>
                </p>
                <p className="text-muted-foreground">no work performed</p>
              </div>
            ) : (
              <p className="font-mono text-sm text-muted-foreground">
                Nothing eligible right now.
              </p>
            )
          ) : (
            <div className="flex items-center gap-2 font-mono text-sm text-muted-foreground">
              <span aria-hidden="true">○</span>
              <span>
                press <span className="text-foreground">Preview next</span> to
                plan a pick
              </span>
              <span className="cursor-block text-primary" aria-hidden="true">
                ▍
              </span>
            </div>
          )}
        </TerminalCard>

        <TerminalCard title="summary">
          <dl className="flex flex-col gap-2 font-mono text-sm">
            <SummaryRow label="mode" value="once" />
            <SummaryRow label="repo" value={repo || '—'} />
            <SummaryRow label="ticket" value={ticket ?? '—'} />
            <SummaryRow label="provider" value={provider} />
          </dl>
        </TerminalCard>
      </div>
    </div>
  )
}

function SummaryRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center gap-4">
      <dt className="w-20 text-muted-foreground">{label}</dt>
      <dd className="text-foreground">{value}</dd>
    </div>
  )
}

function NotStartableNotice({ repo, root }: { repo: string; root?: string }) {
  return (
    <TerminalCard title="run-once" className="max-w-3xl">
      <div className="flex flex-col items-start gap-4">
        <p className="font-sans text-sm leading-relaxed text-muted-foreground">
          {repo
            ? `${repo} is observe-only — the hub can browse its runs but isn't cleared to start loops here yet.`
            : 'No repo checked out yet. Register a repo to run a ticket.'}
        </p>
        <div className="flex flex-wrap items-center gap-2">
          {root && (
            <MakeStartableButton root={root} name={repo} className="font-mono" />
          )}
          <Button asChild variant="outline" size="sm" className="font-mono">
            <Link to="/instances">Manage repos</Link>
          </Button>
        </div>
      </div>
    </TerminalCard>
  )
}
