import { useEffect, useRef, useState, type ReactNode } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Eraser, GitCompare, MoreHorizontal, RotateCcw, X } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/trau/confirm-dialog'
import { ForceResetDialog } from '@/components/trau/force-reset-dialog'
import { cn } from '@/lib/utils'
import {
  CheckpointError,
  clearRun,
  reconcileRepo,
  resetRun,
} from '@/lib/checkpoints'

export type CheckpointNotice = {
  tone: 'success' | 'warn' | 'error'
  text: string
}

type Dialog = 'reset' | 'clear' | 'reconcile' | null

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

function useCheckpointMaintenance({
  repo,
  ticket,
  phase,
  onNotice,
  onConflict,
}: {
  repo: string
  ticket: string
  phase: string
  onNotice?: (notice: CheckpointNotice) => void
  onConflict?: () => void
}) {
  const queryClient = useQueryClient()
  const [dialog, setDialog] = useState<Dialog>(null)
  const [forceRequired, setForceRequired] = useState(false)
  const useForce = phase === 'merged' || forceRequired

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ['runs', repo] })
    void queryClient.invalidateQueries({ queryKey: ['repos'] })
    void queryClient.invalidateQueries({ queryKey: ['run', repo, ticket] })
  }

  // A live loop holding the repo answers every checkpoint mutation with a 409
  // and a plain-language reason. The ledger routes that to its own conflict
  // banner via onConflict; callers without one surface the reason inline. A
  // merged ticket comes back asking to be forced, which reopens the
  // type-to-confirm dialog instead of failing.
  const onError = (error: unknown) => {
    if (error instanceof CheckpointError && error.live) {
      setDialog(null)
      if (onConflict) onConflict()
      else onNotice?.({ tone: 'warn', text: error.message })
      return
    }
    if (error instanceof CheckpointError && error.requiresForce) {
      setForceRequired(true)
      setDialog('reset')
      return
    }
    setDialog(null)
    onNotice?.({ tone: 'error', text: errorText(error) })
  }

  const reset = useMutation({
    mutationFn: (force: boolean) => resetRun(repo, ticket, force),
    onSuccess: () => {
      setDialog(null)
      setForceRequired(false)
      invalidate()
      onNotice?.({ tone: 'success', text: `Reset ${ticket} — re-queued on the tracker.` })
    },
    onError,
  })
  const clear = useMutation({
    mutationFn: () => clearRun(repo, ticket),
    onSuccess: () => {
      setDialog(null)
      invalidate()
      onNotice?.({ tone: 'success', text: `Cleared ${ticket}'s local checkpoint.` })
    },
    onError,
  })
  const reconcile = useMutation({
    mutationFn: () => reconcileRepo(repo),
    onSuccess: (result) => {
      setDialog(null)
      invalidate()
      onNotice?.({
        tone: 'success',
        text:
          result.reconciled.length > 0
            ? `Reconciled ${repo} — cleared ${result.reconciled.join(', ')}.`
            : `Reconciled ${repo} — every checkpoint matches the tracker.`,
      })
    },
    onError,
  })

  const busy = reset.isPending || clear.isPending || reconcile.isPending
  const close = (open: boolean) => {
    if (!open) setDialog(null)
  }

  const dialogs = (
    <>
      {useForce ? (
        <ForceResetDialog
          open={dialog === 'reset'}
          onOpenChange={close}
          ticket={ticket}
          pending={reset.isPending}
          onConfirm={() => reset.mutate(true)}
        />
      ) : (
        <ConfirmDialog
          open={dialog === 'reset'}
          onOpenChange={close}
          title={`Reset ${ticket}?`}
          description={`Drops ${ticket}'s branch and checkpoint and re-queues it on the tracker.`}
          confirmLabel="Reset"
          onConfirm={() => reset.mutate(false)}
        />
      )}
      <ConfirmDialog
        open={dialog === 'clear'}
        onOpenChange={close}
        title={`Clear ${ticket}'s checkpoint?`}
        description={`Forgets ${ticket}'s local checkpoint only — git and the tracker are left untouched. For a ticket finished out-of-band.`}
        confirmLabel="Clear"
        onConfirm={() => clear.mutate()}
      />
      <ConfirmDialog
        open={dialog === 'reconcile'}
        onOpenChange={close}
        title={`Reconcile ${repo}'s checkpoints?`}
        description="Cross-checks every in-flight checkpoint against the tracker and drops any whose issue is already Done or Canceled. Git is left untouched."
        confirmLabel="Reconcile"
        onConfirm={() => reconcile.mutate()}
      />
    </>
  )

  return { open: setDialog, dialogs, busy }
}

const MENU_ITEMS: { label: string; action: Exclude<Dialog, null> }[] = [
  { label: 'Reset', action: 'reset' },
  { label: 'Clear', action: 'clear' },
  { label: 'Reconcile', action: 'reconcile' },
]

export function RunActionsMenu({
  repo,
  ticket,
  phase,
  onNotice,
  onConflict,
}: {
  repo: string
  ticket: string
  phase: string
  onNotice?: (notice: CheckpointNotice) => void
  onConflict?: () => void
}) {
  const { open, dialogs, busy } = useCheckpointMaintenance({
    repo,
    ticket,
    phase,
    onNotice,
    onConflict,
  })
  const [menuOpen, setMenuOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!menuOpen) return
    function onPointerDown(e: PointerEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) setMenuOpen(false)
    }
    document.addEventListener('pointerdown', onPointerDown)
    return () => document.removeEventListener('pointerdown', onPointerDown)
  }, [menuOpen])

  return (
    <div className="relative" ref={ref}>
      <button
        type="button"
        onClick={() => setMenuOpen((o) => !o)}
        aria-expanded={menuOpen}
        aria-label={`Actions for ${ticket}`}
        disabled={busy}
        className="flex size-6 items-center justify-center rounded-md text-muted-foreground hover:bg-secondary hover:text-foreground disabled:opacity-50"
      >
        <MoreHorizontal className="size-4" aria-hidden="true" />
      </button>
      {menuOpen && (
        <ul className="absolute right-0 z-30 mt-1 w-32 overflow-hidden rounded-md border border-border bg-popover py-1 shadow-lg">
          {MENU_ITEMS.map((item) => (
            <li key={item.action}>
              <button
                type="button"
                onClick={() => {
                  setMenuOpen(false)
                  open(item.action)
                }}
                className="flex w-full px-2.5 py-1.5 text-left font-mono text-xs text-foreground hover:bg-secondary"
              >
                {item.label}
              </button>
            </li>
          ))}
        </ul>
      )}
      {dialogs}
    </div>
  )
}

export function RunActionsRow({
  repo,
  ticket,
  phase,
  onNotice,
  leading,
}: {
  repo: string
  ticket: string
  phase: string
  onNotice?: (notice: CheckpointNotice) => void
  leading?: ReactNode
}) {
  const { open, dialogs, busy } = useCheckpointMaintenance({ repo, ticket, phase, onNotice })
  return (
    <div className="flex flex-wrap items-center gap-2">
      {leading}
      <Button
        variant="outline"
        size="sm"
        className="font-mono"
        disabled={busy}
        onClick={() => open('reset')}
      >
        <RotateCcw className="size-3.5" aria-hidden="true" />
        Reset
      </Button>
      <Button
        variant="ghost"
        size="sm"
        className="font-mono"
        disabled={busy}
        onClick={() => open('clear')}
      >
        <Eraser className="size-3.5" aria-hidden="true" />
        Clear
      </Button>
      <Button
        variant="ghost"
        size="sm"
        className="font-mono"
        disabled={busy}
        onClick={() => open('reconcile')}
      >
        <GitCompare className="size-3.5" aria-hidden="true" />
        Reconcile
      </Button>
      {dialogs}
    </div>
  )
}

export function RunResetButton({
  repo,
  ticket,
  phase,
  onNotice,
  onConflict,
}: {
  repo: string
  ticket: string
  phase: string
  onNotice?: (notice: CheckpointNotice) => void
  onConflict?: () => void
}) {
  const { open, dialogs, busy } = useCheckpointMaintenance({
    repo,
    ticket,
    phase,
    onNotice,
    onConflict,
  })
  return (
    <>
      <Button
        variant="outline"
        size="sm"
        className="font-mono"
        disabled={busy}
        onClick={() => open('reset')}
      >
        <RotateCcw className="size-3.5" aria-hidden="true" />
        Reset
      </Button>
      {dialogs}
    </>
  )
}

const NOTICE_TONE: Record<
  CheckpointNotice['tone'],
  { box: string; text: string; glyph: string; dismiss: string }
> = {
  success: {
    box: 'border-done/50 bg-done/12',
    text: 'text-done',
    glyph: '✓',
    dismiss: 'text-done/80 hover:bg-done/12 hover:text-done',
  },
  warn: {
    box: 'border-warn/50 bg-warn/12',
    text: 'text-warn',
    glyph: '⚠',
    dismiss: 'text-warn/80 hover:bg-warn/12 hover:text-warn',
  },
  error: {
    box: 'border-fail/50 bg-fail/12',
    text: 'text-fail',
    glyph: '✗',
    dismiss: 'text-fail/80 hover:bg-fail/12 hover:text-fail',
  },
}

export function NoticeBanner({
  notice,
  onDismiss,
}: {
  notice: CheckpointNotice
  onDismiss: () => void
}) {
  const tone = NOTICE_TONE[notice.tone]
  return (
    <div
      role="status"
      className={cn('flex items-start justify-between gap-3 rounded-lg border px-4 py-3', tone.box)}
    >
      <div className="flex items-start gap-2.5">
        <span aria-hidden="true" className={cn('mt-0.5 font-mono text-sm', tone.text)}>
          {tone.glyph}
        </span>
        <p className={cn('font-mono text-sm leading-relaxed', tone.text)}>{notice.text}</p>
      </div>
      <button
        type="button"
        onClick={onDismiss}
        aria-label="Dismiss"
        className={cn('flex size-6 shrink-0 items-center justify-center rounded-md', tone.dismiss)}
      >
        <X className="size-4" aria-hidden="true" />
      </button>
    </div>
  )
}
