import { useEffect, useState, type KeyboardEvent } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import { AlertTriangle, ArrowRight, Info } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { MakeStartableButton } from '@/components/make-startable-button'
import { useActiveRepo } from './active-repo'
import { ProjectScopeGate } from './project-scope-gate'
import { RepoPicker } from './repo-picker'
import { TargetRepoField } from './target-repo-field'
import { TerminalCard } from './terminal-card'
import { cn } from '@/lib/utils'
import { configQueryOptions } from '@/lib/config'
import { IssueFetchError, issueQueryOptions, type Issue } from '@/lib/issues'
import { startInstance } from '@/lib/instances'

const NO_OVERRIDE = 'default'

function actionError(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

// statusWarning flags a fetched ticket whose status means running it now is
// probably not what the user wants — the real thing the confirm step protects
// against. A ready-but-unlabeled ticket is a softer, non-blocking note.
function statusWarning(
  issue: Issue,
): { tone: 'warn' | 'info'; text: string } | null {
  switch (issue.group) {
    case 'done':
      return { tone: 'warn', text: 'This ticket is already done — running it again may re-open finished work.' }
    case 'canceled':
      return { tone: 'warn', text: 'This ticket is canceled — it is unlikely to be ready to run.' }
    case 'started':
      return { tone: 'warn', text: 'This ticket is in progress — it may already be running elsewhere.' }
    default:
      if (!issue.ready) {
        return { tone: 'info', text: 'This ticket is not carrying the ready label. You can still run it.' }
      }
      return null
  }
}

export function RunOnce() {
  const navigate = useNavigate()

  const { repo: activeRepo, repos: allRepos } = useActiveRepo()
  const startable = allRepos.filter((r) => r.allowed).map((r) => r.name)
  const repo = activeRepo ?? ''
  const canRun = repo !== '' && startable.includes(repo)

  const [ticketId, setTicketId] = useState('')
  const [submittedId, setSubmittedId] = useState('')
  const [provider, setProvider] = useState(NO_OVERRIDE)

  const config = useQuery(configQueryOptions(repo))
  const providers = [NO_OVERRIDE, ...(config.data?.providers ?? [])]

  const issue = useQuery(issueQueryOptions(repo, submittedId))
  const ticket = issue.data

  const start = useMutation({
    mutationFn: () =>
      startInstance({
        repo,
        ticket: submittedId,
        provider: provider === NO_OVERRIDE ? undefined : provider,
      }),
    onSuccess: () => {
      void navigate({
        to: '/live/$repo/$ticket',
        params: { repo, ticket: submittedId },
      })
    },
  })

  useEffect(() => {
    setTicketId('')
    setSubmittedId('')
    setProvider(NO_OVERRIDE)
  }, [repo])

  // The ticket is fetched for confirmation the moment the user commits an id —
  // on Enter or on blur — so there's no extra "fetch" click to reach the confirm.
  function fetchTicket() {
    const id = ticketId.trim().toUpperCase()
    if (id) setSubmittedId(id)
  }

  function onIdKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === 'Enter') {
      e.preventDefault()
      fetchTicket()
    }
  }

  const confirmed = issue.isSuccess && submittedId !== ''
  const fetchKind =
    issue.error instanceof IssueFetchError
      ? issue.error.kind
      : issue.error
        ? 'error'
        : null
  // A ticket from another project was fetched, but this repo is scoped to a
  // different one — running it here would launch into the wrong repo, so it's
  // shown for context but blocked (the server's ownership guard is the backstop).
  const wrongProject = confirmed && !!ticket && !ticket.in_project
  // A repo with no direct tracker Reader can't confirm a ticket, but the loop
  // launches by id through the agent all the same — so allow a confirmless run
  // there. A not-found id stays blocked: that's the typo the confirm guards.
  const canConfirmless = fetchKind === 'no-tracker' && submittedId !== ''
  const canLaunch = (confirmed && !wrongProject) || canConfirmless
  const providerShort = provider === NO_OVERRIDE ? 'default provider' : provider
  const warning = ticket && !wrongProject ? statusWarning(ticket) : null

  const content = !canRun ? (
    <NotStartableNotice
      repo={repo}
      root={allRepos.find((r) => r.name === repo)?.root}
    />
  ) : (
    <div className="flex w-full max-w-xl flex-col gap-6">
      <TerminalCard title="run-once">
        <form
          className="flex flex-col gap-6"
          onSubmit={(e) => {
            e.preventDefault()
            if (confirmed) start.mutate()
          }}
        >
          <TargetRepoField repo={repo} />

          <div className="flex flex-col gap-1.5">
            <label
              htmlFor="ticket-id"
              className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground"
            >
              ticket id
            </label>
            <div className="flex items-center gap-2">
              <input
                id="ticket-id"
                value={ticketId}
                onChange={(e) => {
                  setTicketId(e.target.value)
                  if (submittedId && e.target.value.trim().toUpperCase() !== submittedId) {
                    setSubmittedId('')
                  }
                }}
                onKeyDown={onIdKeyDown}
                onBlur={fetchTicket}
                placeholder="COD-###"
                autoComplete="off"
                spellCheck={false}
                className="w-56 rounded-md border border-border bg-input px-2.5 py-1.5 font-mono text-sm text-foreground placeholder:text-muted-foreground/60 focus-visible:border-ring focus-visible:outline-none"
              />
              <Button
                type="button"
                variant={confirmed ? 'outline' : 'default'}
                size="sm"
                className="font-mono"
                disabled={ticketId.trim() === '' || issue.isFetching}
                onClick={fetchTicket}
              >
                {issue.isFetching ? 'Fetching…' : 'Fetch ticket'}
              </Button>
            </div>
            <p className="font-sans text-xs leading-relaxed text-muted-foreground">
              Press Enter to fetch the ticket for confirmation before anything runs.
            </p>
          </div>

          {issue.isFetching && submittedId && (
            <div
              aria-busy="true"
              className="flex flex-col gap-2 rounded-md border border-border bg-secondary/30 px-3 py-3"
            >
              <div className="flex items-center gap-3">
                <span className="h-3 w-16 animate-pulse rounded bg-muted" />
                <span className="h-3 w-2/3 animate-pulse rounded bg-muted" />
              </div>
              <span className="h-3 w-24 animate-pulse rounded bg-muted" />
            </div>
          )}

          {!issue.isFetching && issue.error && (
            <FetchError error={issue.error} id={submittedId} />
          )}

          {confirmed && ticket && (
            <div
              className={cn(
                'flex flex-col gap-3 rounded-md border px-3 py-3',
                wrongProject
                  ? 'border-fail/40 bg-fail/5'
                  : 'border-primary/40 bg-primary/5',
              )}
            >
              <div className="flex flex-wrap items-center gap-2">
                <span
                  className={cn(
                    'font-mono text-sm',
                    wrongProject ? 'text-fail' : 'text-primary',
                  )}
                >
                  {ticket.id}
                </span>
                <span className="font-sans text-sm text-foreground">
                  {ticket.title}
                </span>
              </div>
              <dl className="flex flex-wrap gap-x-6 gap-y-1 font-mono text-xs">
                <div className="flex items-center gap-2">
                  <dt className="text-muted-foreground">status</dt>
                  <dd className="text-foreground">{ticket.status || '—'}</dd>
                </div>
                {ticket.project && (
                  <div className="flex items-center gap-2">
                    <dt className="text-muted-foreground">project</dt>
                    <dd
                      className={wrongProject ? 'text-fail' : 'text-foreground'}
                    >
                      {ticket.project}
                    </dd>
                  </div>
                )}
                {ticket.labels.length > 0 && (
                  <div className="flex items-center gap-2">
                    <dt className="text-muted-foreground">labels</dt>
                    <dd className="flex flex-wrap gap-1.5">
                      {ticket.labels.map((label) => (
                        <span
                          key={label}
                          className="rounded border border-border bg-muted/60 px-1.5 py-0.5 text-muted-foreground"
                        >
                          {label}
                        </span>
                      ))}
                    </dd>
                  </div>
                )}
              </dl>
              {wrongProject ? (
                <p
                  role="alert"
                  className="flex items-start gap-2 rounded-md border border-fail/40 bg-fail/5 px-2.5 py-2 font-sans text-xs leading-relaxed text-fail"
                >
                  <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
                  <span>
                    {ticket.id} belongs to another project
                    {ticket.project ? ` (${ticket.project})` : ''}, not{' '}
                    {repo}. Switch to that project's repo to run it.
                  </span>
                </p>
              ) : (
                warning && (
                  <p
                    className={cn(
                      'flex items-start gap-2 rounded-md border px-2.5 py-2 font-sans text-xs leading-relaxed',
                      warning.tone === 'warn'
                        ? 'border-warn/40 bg-warn/5 text-warn'
                        : 'border-border bg-secondary/40 text-muted-foreground',
                    )}
                  >
                    {warning.tone === 'warn' ? (
                      <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
                    ) : (
                      <Info className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
                    )}
                    <span>{warning.text}</span>
                  </p>
                )
              )}
            </div>
          )}

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

          <div className="flex flex-col gap-3 border-t border-border pt-4">
            {canLaunch && (
              <p className="flex items-center gap-2 font-mono text-xs text-muted-foreground">
                <ArrowRight className="size-3.5 text-teal" aria-hidden="true" />
                <span>
                  run{' '}
                  <span className="text-primary">
                    {ticket?.id ?? submittedId}
                  </span>{' '}
                  on <span className="text-foreground">{repo}</span> with{' '}
                  <span className="text-foreground">{providerShort}</span>
                </span>
              </p>
            )}
            <div>
              <Button
                type="submit"
                size="sm"
                className="font-mono"
                disabled={!canLaunch || start.isPending}
              >
                {start.isPending ? 'Launching…' : 'Run once'}
              </Button>
            </div>
            {!canLaunch && !wrongProject && (
              <p className="font-sans text-xs leading-relaxed text-muted-foreground">
                Fetch and confirm the ticket first.
              </p>
            )}
            {start.error && (
              <p className="font-mono text-xs text-destructive">
                {actionError(start.error)}
              </p>
            )}
          </div>
        </form>
      </TerminalCard>
    </div>
  )

  return <ProjectScopeGate action="run a ticket">{content}</ProjectScopeGate>
}

function FetchError({ error, id }: { error: unknown; id: string }) {
  const kind = error instanceof IssueFetchError ? error.kind : 'error'

  if (kind === 'not-found') {
    return (
      <div
        role="alert"
        className="flex items-start gap-2.5 rounded-md border border-fail/40 bg-fail/5 px-3 py-3"
      >
        <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-fail" aria-hidden="true" />
        <div className="flex flex-col gap-0.5">
          <p className="font-mono text-sm text-foreground">{id} not found</p>
          <p className="font-sans text-xs leading-relaxed text-muted-foreground">
            Check the ticket id and that it exists in this repo's tracker.
          </p>
        </div>
      </div>
    )
  }

  if (kind === 'no-tracker') {
    return (
      <div
        role="alert"
        className="flex items-start gap-2.5 rounded-md border border-warn/40 bg-warn/5 px-3 py-3"
      >
        <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-warn" aria-hidden="true" />
        <div className="flex flex-col gap-1">
          <p className="font-mono text-sm text-foreground">
            No direct tracker for this repo
          </p>
          <p className="font-sans text-xs leading-relaxed text-muted-foreground">
            Confirming a ticket needs direct tracker credentials. You can still
            launch by id, or add credentials in{' '}
            <Link to="/settings" className="text-primary hover:underline">
              settings
            </Link>
            .
          </p>
        </div>
      </div>
    )
  }

  return (
    <p className="font-mono text-sm text-destructive">{actionError(error)}</p>
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
