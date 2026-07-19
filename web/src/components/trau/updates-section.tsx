import { useEffect, useState, type ReactNode } from 'react'
import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import { ExternalLink, RefreshCw, RotateCw, TriangleAlert } from 'lucide-react'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/trau/confirm-dialog'
import { TerminalCard } from '@/components/trau/terminal-card'
import { Button } from '@/components/ui/button'
import { instancesQueryOptions, type Instance } from '@/lib/instances'
import { queueQueryOptions } from '@/lib/queue'
import {
  applyUpdate,
  canApply,
  checkForUpdates,
  checkedAgo,
  currentHubMark,
  markRestarted,
  restartHub,
  takeRestartedVersion,
  updateQueryOptions,
  versionLabel,
  waitForSuccessor,
  type HubMark,
  type UpdateStatus,
} from '@/lib/update'
import { cn } from '@/lib/utils'

type Pending = 'restart' | 'update'

const RELEASES_URL = 'https://github.com/RomkaLTU/trau/releases'

export function UpdatesSection() {
  const queryClient = useQueryClient()
  const update = useQuery(updateQueryOptions)
  const [pending, setPending] = useState<Pending | null>(null)
  const [restarting, setRestarting] = useState(false)
  const [restartError, setRestartError] = useState<string | null>(null)
  const [applyMark, setApplyMark] = useState<HubMark | null>(null)
  const [restarted, setRestarted] = useState(false)

  useEffect(() => setRestarted(takeRestartedVersion() !== null), [])

  // The Toaster subscribes after this subtree mounts and replays nothing, so the
  // confirmation waits for the successor's own status rather than firing on
  // mount — which also lets it name the version actually serving the page now.
  useEffect(() => {
    if (!restarted || !update.data) return
    toast(`Hub restarted — now ${versionLabel(update.data.running)}`)
    setRestarted(false)
  }, [restarted, update.data])

  function publish(status: UpdateStatus) {
    queryClient.setQueryData(updateQueryOptions.queryKey, status)
  }

  async function pickUpSuccessor(before: HubMark) {
    setRestarting(true)
    setRestartError(null)
    try {
      const after = await waitForSuccessor(before)
      markRestarted(after.version)
      window.location.reload()
    } catch (err) {
      setRestarting(false)
      setRestartError(err instanceof Error ? err.message : String(err))
    }
  }

  const check = useMutation({
    mutationFn: checkForUpdates,
    onSuccess: publish,
    onError: (err: Error) => toast.error(err.message),
  })

  const restart = useMutation({
    mutationFn: async () => {
      const before = await currentHubMark()
      await restartHub()
      return before
    },
    onSuccess: (before) => void pickUpSuccessor(before),
    onError: (err: Error) => toast.error(err.message),
  })

  const apply = useMutation({
    mutationFn: async () => {
      const before = await currentHubMark()
      return { before, status: await applyUpdate() }
    },
    onSuccess: ({ before, status }) => {
      setApplyMark(before)
      publish(status)
    },
    onError: (err: Error) => toast.error(err.message),
  })

  // A successful apply ends in a restart the client never asked for, so the
  // apply is watched two ways: the successor may answer /update on a new version
  // before the poll ever misses, or the poll fails across the gap and the health
  // probe takes over. Either way the page reloads onto the new assets.
  useEffect(() => {
    if (!applyMark || restarting) return
    if (update.isError) {
      void pickUpSuccessor(applyMark)
      return
    }
    const status = update.data
    if (!status) return
    if (status.running !== applyMark.version) {
      markRestarted(status.running)
      window.location.reload()
      return
    }
    if (status.applyState.state !== 'running') setApplyMark(null)
  }, [applyMark, restarting, update.data, update.isError])

  const status = update.data
  const applying = status?.applyState.state === 'running'
  const busy = applying || restarting || restart.isPending || apply.isPending

  return (
    <section id="updates" className="scroll-mt-6">
      <TerminalCard title="updates" bodyClassName="p-0">
        <div className="flex flex-col">
          <p className="border-b border-border/60 px-4 py-2 text-xs leading-relaxed text-muted-foreground">
            The hub keeps serving the version it booted with. An upgrade lands on
            disk first and only a restart picks it up.
          </p>

          {!status ? (
            <div className="flex flex-col gap-2 p-4" aria-busy="true">
              <div className="h-3 w-48 animate-pulse rounded bg-secondary" />
              <div className="h-3 w-32 animate-pulse rounded bg-secondary/70" />
            </div>
          ) : (
            <>
              <Row label="running">
                <span className="font-mono text-xs text-foreground">
                  {versionLabel(status.running)}
                </span>
              </Row>

              {status.restartPending && (
                <Row label="on disk">
                  <span className="font-mono text-xs text-warn">
                    {versionLabel(status.onDisk)} — restart to apply
                  </span>
                </Row>
              )}

              <Row label="latest">
                {!status.checksEnabled ? (
                  <span className="text-xs text-muted-foreground">
                    Release checks are off — set{' '}
                    <span className="font-mono text-foreground">
                      UPDATE_CHECK
                    </span>{' '}
                    to 1 to see new releases.
                  </span>
                ) : (
                  <span className="flex flex-wrap items-center gap-2">
                    {status.releaseUrl ? (
                      <a
                        href={status.releaseUrl}
                        target="_blank"
                        rel="noreferrer"
                        className="inline-flex items-center gap-1 font-mono text-xs text-primary underline-offset-2 hover:underline"
                      >
                        {versionLabel(status.latest)}
                        <ExternalLink className="size-3" aria-hidden="true" />
                      </a>
                    ) : (
                      <span className="font-mono text-xs text-faint">
                        not checked yet
                      </span>
                    )}
                    <span className="font-mono text-[0.7rem] text-faint">
                      checked {checkedAgo(status.checkedAt)}
                    </span>
                    <Button
                      variant="outline"
                      size="sm"
                      className="font-mono text-xs"
                      disabled={check.isPending}
                      onClick={() => check.mutate()}
                    >
                      <RefreshCw
                        className={cn('size-3.5', check.isPending && 'animate-spin')}
                        aria-hidden="true"
                      />
                      {check.isPending ? 'checking' : 'Check for updates'}
                    </Button>
                  </span>
                )}
              </Row>

              {status.installMethod !== 'brew' &&
                (status.updateAvailable || status.restartPending) && (
                  <Row label="install">
                    <span className="text-xs leading-relaxed text-muted-foreground">
                      trau was not installed by Homebrew, so it cannot update
                      itself. Update it the way you installed it, then restart
                      the hub — releases are at{' '}
                      <a
                        href={status.releaseUrl || RELEASES_URL}
                        target="_blank"
                        rel="noreferrer"
                        className="font-mono text-primary underline-offset-2 hover:underline"
                      >
                        {status.releaseUrl || RELEASES_URL}
                      </a>
                      .
                    </span>
                  </Row>
                )}

              {status.applyState.state === 'failed' && (
                <div className="flex flex-col gap-2 border-b border-border/60 px-4 py-3">
                  <p
                    className="inline-flex items-center gap-2 font-mono text-xs text-fail"
                    role="alert"
                  >
                    <TriangleAlert className="size-3.5" aria-hidden="true" />
                    brew upgrade failed
                  </p>
                  <pre className="max-h-56 overflow-auto rounded-md border border-border bg-input px-3 py-2 font-mono text-[0.7rem] leading-relaxed text-muted-foreground">
                    {status.applyState.message}
                  </pre>
                </div>
              )}

              {restartError && (
                <div className="border-b border-border/60 px-4 py-3">
                  <p
                    className="inline-flex items-center gap-2 font-mono text-xs text-fail"
                    role="alert"
                  >
                    <TriangleAlert className="size-3.5" aria-hidden="true" />
                    {restartError}
                  </p>
                </div>
              )}

              <div className="flex flex-wrap items-center gap-2 px-4 py-3">
                {canApply(status) && (
                  <Button
                    size="sm"
                    className="font-mono text-xs"
                    disabled={busy}
                    onClick={() => setPending('update')}
                  >
                    {applying ? 'Updating via Homebrew…' : 'Update now'}
                  </Button>
                )}
                <Button
                  variant="outline"
                  size="sm"
                  className="font-mono text-xs"
                  disabled={busy}
                  onClick={() => setPending('restart')}
                >
                  <RotateCw
                    className={cn('size-3.5', restarting && 'animate-spin')}
                    aria-hidden="true"
                  />
                  {restarting ? 'Restarting hub…' : 'Restart hub'}
                </Button>
              </div>
            </>
          )}
        </div>
      </TerminalCard>

      <RestartConfirm
        pending={pending}
        onCancel={() => setPending(null)}
        onConfirm={() => {
          const action = pending
          setPending(null)
          if (action === 'update') apply.mutate()
          if (action === 'restart') restart.mutate()
        }}
      />
    </section>
  )
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-border/60 px-4 py-2.5">
      <span className="w-20 shrink-0 font-mono text-[0.7rem] uppercase tracking-[0.15em] text-muted-foreground/60">
        {label}
      </span>
      {children}
    </div>
  )
}

// RestartConfirm names what a restart interrupts before it happens. The queues
// are only fetched while the dialog is open — one request per repo is worth it
// for a confirmation and not for a settings page nobody is restarting from.
function RestartConfirm({
  pending,
  onCancel,
  onConfirm,
}: {
  pending: Pending | null
  onCancel: () => void
  onConfirm: () => void
}) {
  const open = pending !== null
  const instances = useQuery({ ...instancesQueryOptions, enabled: open })
  const repos = instances.data?.repos ?? []
  const queues = useQueries({
    queries: repos.map((repo) => ({
      ...queueQueryOptions(repo.name),
      enabled: open,
    })),
  })

  const active = instances.data?.instances ?? []
  const draining = repos
    .map((repo) => repo.name)
    .filter((_, i) => queues[i]?.data?.draining)

  return (
    <ConfirmDialog
      open={open}
      onOpenChange={(next) => !next && onCancel()}
      windowTitle={pending === 'update' ? 'update trau' : 'restart hub'}
      title={
        pending === 'update' ? 'Update and restart the hub?' : 'Restart the hub?'
      }
      description={<RestartImpact active={active} draining={draining} />}
      confirmLabel={pending === 'update' ? 'Update now' : 'Restart'}
      onConfirm={onConfirm}
    />
  )
}

function RestartImpact({
  active,
  draining,
}: {
  active: Instance[]
  draining: string[]
}) {
  if (active.length === 0 && draining.length === 0) {
    return <>The hub will be briefly unavailable.</>
  }

  return (
    <>
      <span className="block">
        Work in flight pauses blamelessly and can be resumed afterwards.
      </span>
      {active.length > 0 && (
        <span className="mt-2 block font-mono text-xs text-foreground">
          {active.map((instance) => (
            <span key={instance.pid} className="block truncate">
              {instance.repo}
              {instance.ticket ? ` · ${instance.ticket}` : ''}
            </span>
          ))}
        </span>
      )}
      {draining.length > 0 && (
        <span className="mt-2 block">
          Draining:{' '}
          <span className="font-mono text-foreground">
            {draining.join(', ')}
          </span>
        </span>
      )}
    </>
  )
}
