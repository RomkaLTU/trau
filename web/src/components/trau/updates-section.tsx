import { useEffect, useState, type ReactNode } from 'react'
import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ExternalLink, RefreshCw, RotateCw, TriangleAlert } from 'lucide-react'
import { toast } from 'sonner'

import { Markdown } from '@/components/markdown'
import { ConfirmDialog } from '@/components/trau/confirm-dialog'
import { TerminalCard } from '@/components/trau/terminal-card'
import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { instancesQueryOptions, type Instance } from '@/lib/instances'
import { queueQueryOptions } from '@/lib/queue'
import {
  CHANGELOG_URL,
  RELEASES_URL,
  applyUpdate,
  canApply,
  checkForUpdates,
  checkedAgo,
  currentHubMark,
  markRestarted,
  restartHub,
  switchChannel,
  switchInFlight,
  takeRestartedVersion,
  updateQueryOptions,
  versionLabel,
  waitForSuccessor,
  type Channel,
  type HubMark,
  type UpdateStatus,
} from '@/lib/update'
import { cn } from '@/lib/utils'

// The three channel actions all POST the same endpoint: dev rebuilds a repo the
// hub is not running from, rebuild does the same for the tree it already runs,
// and release restarts onto the install on PATH.
type Pending = 'restart' | 'update' | 'dev' | 'rebuild' | 'release'

// What the confirm dialog says and what confirming it does, so an action is
// described in one place rather than once for its copy and once for its call.
interface ConfirmAction {
  windowTitle: string
  title: string
  confirmLabel: string
  note?: string
  run: () => void
}

// Settings renders its config rows only once the config query lands, so a plain
// hash cannot aim from here — the ?q= landing search + highlight can.
const SELF_RELOAD_KEY = 'HUB_SELF_RELOAD'

export function UpdatesSection() {
  const queryClient = useQueryClient()
  const update = useQuery(updateQueryOptions)
  const [pending, setPending] = useState<Pending | null>(null)
  const [restarting, setRestarting] = useState(false)
  const [restartError, setRestartError] = useState<string | null>(null)
  const [applyMark, setApplyMark] = useState<HubMark | null>(null)
  const [switchMark, setSwitchMark] = useState<HubMark | null>(null)
  const [switchRepo, setSwitchRepo] = useState('')
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

  const status = update.data
  const eligible = status?.channelRepos ?? []
  const target =
    eligible.find((repo) => repo.root === switchRepo)?.root ??
    eligible[0]?.root ??
    ''

  const changeChannel = useMutation({
    mutationFn: async ({
      channel,
      repoRoot,
    }: {
      channel: Channel
      repoRoot: string
    }) => {
      const before = await currentHubMark()
      await switchChannel(channel, repoRoot)
      return before
    },
    onSuccess: (before) => {
      setSwitchMark(before)
      void queryClient.invalidateQueries({
        queryKey: updateQueryOptions.queryKey,
      })
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
    if (!status) return
    if (status.running !== applyMark.version) {
      markRestarted(status.running)
      window.location.reload()
      return
    }
    if (status.applyState.state !== 'running') setApplyMark(null)
  }, [applyMark, restarting, status, update.isError])

  // The rebuild runs on the hub, so the switch is followed the same two ways an
  // apply is: the successor answers on a new build, or the poll fails across the
  // gap and the health probe takes over. A failed build restarts nothing and
  // simply hands the tail back.
  useEffect(() => {
    if (!switchMark || restarting) return
    if (update.isError) {
      void pickUpSuccessor(switchMark)
      return
    }
    const phase = status?.channelSwitch.state
    if (phase === 'restarting') {
      void pickUpSuccessor(switchMark)
      return
    }
    if (phase === 'failed') setSwitchMark(null)
  }, [switchMark, restarting, status, update.isError])

  const applying = status?.applyState.state === 'running'
  const switching = switchInFlight(status)
  const busy =
    applying ||
    switching ||
    restarting ||
    restart.isPending ||
    apply.isPending ||
    changeChannel.isPending

  // launchd restarts a supervised hub from the binary its plist names, so the
  // switch rewrites the plist on the way — worth saying before the click, since
  // it outlives the restart the dialog is otherwise about.
  const supervisedNote = status?.supervised
    ? 'launchd supervises this hub — its LaunchAgent is pointed at the new binary before the restart, so the successor it keeps alive is the one you picked.'
    : undefined

  const actions: Record<Pending, ConfirmAction> = {
    restart: {
      windowTitle: 'restart hub',
      title: 'Restart the hub?',
      confirmLabel: 'Restart',
      run: () => restart.mutate(),
    },
    update: {
      windowTitle: 'update trau',
      title: 'Update and restart the hub?',
      confirmLabel: 'Update now',
      run: () => apply.mutate(),
    },
    dev: {
      windowTitle: 'switch to dev',
      title: 'Rebuild and restart onto the dev build?',
      confirmLabel: 'Rebuild & restart',
      note: supervisedNote,
      run: () => changeChannel.mutate({ channel: 'dev', repoRoot: target }),
    },
    rebuild: {
      windowTitle: 'rebuild & restart',
      title: 'Rebuild this tree and restart onto it?',
      confirmLabel: 'Rebuild & restart',
      note: supervisedNote,
      run: () =>
        changeChannel.mutate({
          channel: 'dev',
          repoRoot: status?.channelRepo ?? '',
        }),
    },
    release: {
      windowTitle: 'switch to release',
      title: 'Restart onto the installed release?',
      confirmLabel: 'Switch to release',
      note: supervisedNote,
      run: () => changeChannel.mutate({ channel: 'release', repoRoot: '' }),
    },
  }
  const confirming = actions[pending ?? 'restart']

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

              <Row label="channel">
                <ChannelAction
                  status={status}
                  busy={busy}
                  switching={switching}
                  target={target}
                  onTarget={setSwitchRepo}
                  onSwitch={setPending}
                />
              </Row>

              {status.selfReloadPending && (
                <Row label="reload">
                  <span className="text-xs text-muted-foreground">
                    <span className="font-mono text-foreground">
                      {repoLabel(status.selfReloadPending)}
                    </span>{' '}
                    asked for a self-reload — it applies when runs finish.
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
                    {status.channel === 'dev' && (
                      <span className="text-xs text-muted-foreground">
                        dev channel — release updates apply after switching back.
                      </span>
                    )}
                  </span>
                )}
              </Row>

              <ReleaseNotes latest={status.latest} notes={status.latestNotes} />

              {status.installMethod !== 'brew' &&
                (status.updateAvailable || status.restartPending) && (
                  <Row label="install">
                    <span className="text-xs leading-relaxed text-muted-foreground">
                      {status.upgradeCommand ? (
                        <>
                          trau was installed with{' '}
                          <span className="font-mono text-foreground">
                            {status.installMethod}
                          </span>
                          , which owns updating it. Run{' '}
                          <span className="font-mono text-foreground">
                            {status.upgradeCommand}
                          </span>
                          , then restart the hub.
                        </>
                      ) : (
                        <>
                          trau was not installed by a package manager it knows,
                          so it cannot update itself. Update it the way you
                          installed it, then restart the hub — releases are at{' '}
                          <a
                            href={status.releaseUrl || RELEASES_URL}
                            target="_blank"
                            rel="noreferrer"
                            className="font-mono text-primary underline-offset-2 hover:underline"
                          >
                            {status.releaseUrl || RELEASES_URL}
                          </a>
                          .
                        </>
                      )}
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

              {status.channelSwitch.state === 'failed' && (
                <div className="flex flex-col gap-2 border-b border-border/60 px-4 py-3">
                  <p
                    className="inline-flex items-center gap-2 font-mono text-xs text-fail"
                    role="alert"
                  >
                    <TriangleAlert className="size-3.5" aria-hidden="true" />
                    the rebuild failed — the hub kept the build it was running
                  </p>
                  <pre className="max-h-56 overflow-auto rounded-md border border-border bg-input px-3 py-2 font-mono text-[0.7rem] leading-relaxed text-muted-foreground">
                    {status.channelSwitch.message}
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
        open={pending !== null}
        action={confirming}
        onCancel={() => setPending(null)}
        onConfirm={() => {
          setPending(null)
          confirming.run()
        }}
      />
    </section>
  )
}

function repoLabel(root: string): string {
  return root.split('/').filter(Boolean).pop() ?? root
}

// ChannelAction shows which build the hub runs and offers the way off it. On a
// release build that is the switch to a dev one, against the repos the hub would
// actually accept, so the picker only appears when the choice is real; with
// none, the action stays visible but disabled and points at the key that opens
// it. On a dev build it is the rebuild of the tree already running and the way
// back to the release install, disabled when PATH holds none to go back to.
function ChannelAction({
  status,
  busy,
  switching,
  target,
  onTarget,
  onSwitch,
}: {
  status: UpdateStatus
  busy: boolean
  switching: boolean
  target: string
  onTarget: (root: string) => void
  onSwitch: (action: Pending) => void
}) {
  if (status.channel === 'dev') {
    return (
      <span className="flex flex-wrap items-center gap-2">
        <span className="font-mono text-xs text-teal">dev</span>
        {status.channelRepo && (
          <span className="font-mono text-[0.7rem] text-faint">
            built from {repoLabel(status.channelRepo)}
          </span>
        )}
        <Button
          variant="outline"
          size="sm"
          className="font-mono text-xs"
          disabled={busy || !status.channelRepo}
          onClick={() => onSwitch('rebuild')}
        >
          {switching ? 'Rebuilding…' : 'Rebuild & restart'}
        </Button>
        <Button
          variant="outline"
          size="sm"
          className="font-mono text-xs"
          disabled={busy || !status.releaseBinary}
          onClick={() => onSwitch('release')}
        >
          Switch to release
        </Button>
        {!status.releaseBinary && (
          <span className="text-xs leading-relaxed text-muted-foreground">
            No release install found — nothing on PATH outside your registered
            repos to go back to.
          </span>
        )}
      </span>
    )
  }

  const eligible = status.channelRepos

  return (
    <span className="flex flex-wrap items-center gap-2">
      <span className="font-mono text-xs text-foreground">release</span>
      {eligible.length > 1 && (
        <Select value={target} onValueChange={onTarget} disabled={busy}>
          <SelectTrigger size="sm" className="font-mono text-xs">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {eligible.map((repo) => (
              <SelectItem key={repo.root} value={repo.root}>
                {repo.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      )}
      <Button
        variant="outline"
        size="sm"
        className="font-mono text-xs"
        disabled={busy || eligible.length === 0}
        onClick={() => onSwitch('dev')}
      >
        {switching ? 'Rebuilding…' : 'Switch to dev'}
      </Button>
      {eligible.length === 0 && (
        <span className="text-xs leading-relaxed text-muted-foreground">
          No registered repo allows it — set{' '}
          <Link
            to="/settings"
            search={{ q: SELF_RELOAD_KEY }}
            className="font-mono text-primary underline-offset-2 hover:underline"
          >
            {SELF_RELOAD_KEY}
          </Link>{' '}
          to 1 for your trau checkout.
        </span>
      )}
    </span>
  )
}

// ReleaseNotes carries the GitHub body for the latest tag, which describes that
// tag and not a pending upgrade — once an update lands it reads as the running
// version's own notes. The changelog link stands alone whenever there is no body
// to show, which is every case where release checks are off.
export function ReleaseNotes({
  latest,
  notes,
}: {
  latest: string
  notes: string
}) {
  return (
    <div className="flex flex-col gap-2 border-b border-border/60 px-4 py-3">
      {latest && notes && (
        <>
          <p className="font-mono text-[0.7rem] uppercase tracking-[0.15em] text-muted-foreground/60">
            What's new in {versionLabel(latest)}
          </p>
          <div className="max-h-64 overflow-y-auto rounded-md border border-border bg-input px-3 py-2">
            <Markdown className="text-xs">{notes}</Markdown>
          </div>
        </>
      )}
      <span className="text-xs text-muted-foreground">
        Full changelog →{' '}
        <a
          href={CHANGELOG_URL}
          target="_blank"
          rel="noreferrer"
          className="font-mono text-primary underline-offset-2 hover:underline"
        >
          {CHANGELOG_URL}
        </a>
      </span>
    </div>
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

// RestartConfirm names what a restart interrupts before it happens — a channel
// switch ends in one exactly like the Restart button does. The queues are only
// fetched while the dialog is open — one request per repo is worth it for a
// confirmation and not for a settings page nobody is restarting from.
function RestartConfirm({
  open,
  action,
  onCancel,
  onConfirm,
}: {
  open: boolean
  action: ConfirmAction
  onCancel: () => void
  onConfirm: () => void
}) {
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
      windowTitle={action.windowTitle}
      title={action.title}
      description={
        <>
          <RestartImpact active={active} draining={draining} />
          {action.note && <span className="mt-2 block">{action.note}</span>}
        </>
      }
      confirmLabel={action.confirmLabel}
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
