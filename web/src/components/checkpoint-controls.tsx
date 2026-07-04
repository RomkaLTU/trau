import type { ReactNode } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { RefreshCw, RotateCcw, Trash2 } from 'lucide-react'

import { Button, buttonVariants } from '@/components/ui/button'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from '@/components/ui/alert-dialog'
import { clearRun, reconcileRepo, resetRun } from '@/lib/checkpoints'

function ConfirmDialog({
  trigger,
  title,
  description,
  confirmLabel,
  destructive = false,
  onConfirm,
}: {
  trigger: ReactNode
  title: string
  description: string
  confirmLabel: string
  destructive?: boolean
  onConfirm: () => void
}) {
  return (
    <AlertDialog>
      <AlertDialogTrigger asChild>{trigger}</AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction
            onClick={onConfirm}
            className={destructive ? buttonVariants({ variant: 'destructive' }) : undefined}
          >
            {confirmLabel}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

function actionError(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

// CheckpointControls exposes the per-ticket housekeeping mutations — reset and
// clear — each behind a confirmation. Resetting an already-merged ticket confirms
// as a force, matching the CLI's --force. While a loop is live in the repo the
// controls stand down: the server refuses the mutation and there is nothing safe
// to offer here.
export function CheckpointControls({
  repo,
  ticket,
  phase,
  live,
}: {
  repo: string
  ticket: string
  phase: string
  live: boolean
}) {
  const queryClient = useQueryClient()
  const merged = phase === 'merged'

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ['runs', repo] })
    void queryClient.invalidateQueries({ queryKey: ['repos'] })
    void queryClient.invalidateQueries({ queryKey: ['run', repo, ticket] })
  }

  const reset = useMutation({
    mutationFn: () => resetRun(repo, ticket, merged),
    onSuccess: invalidate,
  })
  const clear = useMutation({
    mutationFn: () => clearRun(repo, ticket),
    onSuccess: invalidate,
  })

  if (live) {
    return (
      <p className="text-xs text-muted-foreground">
        Checkpoint actions are paused while a loop is live in {repo}.
      </p>
    )
  }

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <ConfirmDialog
          trigger={
            <Button variant="outline" size="sm" disabled={reset.isPending}>
              <RotateCcw />
              {reset.isPending ? 'Resetting…' : 'Reset'}
            </Button>
          }
          title={merged ? `Force-reset ${ticket}?` : `Reset ${ticket}?`}
          description={
            merged
              ? `${ticket} is already merged. Resetting drops its shipped branch and re-queues it on the tracker — this cannot be undone.`
              : `Drops ${ticket}'s branch and checkpoint and re-queues it on the tracker.`
          }
          confirmLabel={merged ? 'Force reset' : 'Reset'}
          destructive={merged}
          onConfirm={() => reset.mutate()}
        />
        <ConfirmDialog
          trigger={
            <Button variant="outline" size="sm" disabled={clear.isPending}>
              <Trash2 />
              {clear.isPending ? 'Clearing…' : 'Clear'}
            </Button>
          }
          title={`Clear ${ticket}'s checkpoint?`}
          description={`Forgets ${ticket}'s local checkpoint only — git and the tracker are left untouched. For a ticket finished out-of-band.`}
          confirmLabel="Clear"
          onConfirm={() => clear.mutate()}
        />
      </div>
      {reset.isSuccess && (
        <p className="text-xs text-emerald-600 dark:text-emerald-400">
          Reset {ticket} — re-queued on the tracker.
        </p>
      )}
      {clear.isSuccess && (
        <p className="text-xs text-emerald-600 dark:text-emerald-400">
          Cleared {ticket}'s local checkpoint.
        </p>
      )}
      {reset.error && <p className="text-xs text-destructive">{actionError(reset.error)}</p>}
      {clear.error && <p className="text-xs text-destructive">{actionError(clear.error)}</p>}
    </div>
  )
}

// ReconcileButton runs the repo-level reconcile behind a confirmation: it
// cross-checks every in-flight checkpoint against the tracker and drops the ones
// the tracker now considers finished. It stands down while a loop is live.
export function ReconcileButton({ repo, live }: { repo: string; live: boolean }) {
  const queryClient = useQueryClient()
  const reconcile = useMutation({
    mutationFn: () => reconcileRepo(repo),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['runs', repo] })
      void queryClient.invalidateQueries({ queryKey: ['repos'] })
    },
  })

  return (
    <div className="flex flex-col gap-1.5">
      <ConfirmDialog
        trigger={
          <Button variant="outline" size="sm" disabled={live || reconcile.isPending}>
            <RefreshCw />
            {reconcile.isPending ? 'Reconciling…' : 'Reconcile'}
          </Button>
        }
        title={`Reconcile ${repo}'s checkpoints?`}
        description={`Cross-checks every in-flight checkpoint against the tracker and drops any whose issue is already Done or Canceled. Git is left untouched.`}
        confirmLabel="Reconcile"
        onConfirm={() => reconcile.mutate()}
      />
      {reconcile.isSuccess &&
        (reconcile.data.reconciled.length > 0 ? (
          <p className="text-xs text-emerald-600 dark:text-emerald-400">
            Cleared {reconcile.data.reconciled.length} stale checkpoint
            {reconcile.data.reconciled.length === 1 ? '' : 's'}:{' '}
            {reconcile.data.reconciled.join(', ')}.
          </p>
        ) : (
          <p className="text-xs text-muted-foreground">
            Nothing stale — every checkpoint matches the tracker.
          </p>
        ))}
      {reconcile.error && (
        <p className="text-xs text-destructive">{actionError(reconcile.error)}</p>
      )}
    </div>
  )
}
