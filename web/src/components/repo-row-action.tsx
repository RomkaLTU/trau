import { useState, type ReactNode } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Loader2, type LucideIcon } from 'lucide-react'

import { ConfirmDialog } from '@/components/trau/confirm-dialog'
import { cn } from '@/lib/utils'

// RepoRowAction is the mechanics both removals on a repo row share: a small text
// button that confirms first, then refreshes the repo list off the result. Only
// the copy, the icon and the call differ between them. A blocked reason disables
// the button and rides along as its tooltip, so the row says why the hub would
// refuse instead of handing back a failed request. successMessage reads the call's
// result when the outcome is only known afterwards, so a partial removal can name
// what it left behind.
export function RepoRowAction<T>({
  icon: Icon,
  label,
  pendingLabel,
  blocked,
  destructive = false,
  windowTitle,
  title,
  description,
  confirmLabel,
  successMessage,
  action,
}: {
  icon: LucideIcon
  label: string
  pendingLabel: string
  blocked?: string | null
  destructive?: boolean
  windowTitle: string
  title: string
  description: ReactNode
  confirmLabel: string
  successMessage: string | ((result: T) => string)
  action: () => Promise<T>
}) {
  const queryClient = useQueryClient()
  const [confirming, setConfirming] = useState(false)

  const run = useMutation({
    mutationFn: action,
    onSuccess: (result) => {
      toast.success(
        typeof successMessage === 'string'
          ? successMessage
          : successMessage(result),
      )
      void queryClient.invalidateQueries({ queryKey: ['repos'] })
      void queryClient.invalidateQueries({ queryKey: ['instances'] })
      void queryClient.invalidateQueries({ queryKey: ['projects'] })
    },
    onError: (err) => toast.error(err.message),
  })

  return (
    <>
      <button
        type="button"
        onClick={() => setConfirming(true)}
        disabled={blocked != null || run.isPending}
        title={blocked ?? undefined}
        className={cn(
          'inline-flex w-fit shrink-0 items-center gap-1.5 text-xs text-muted-foreground transition-colors disabled:opacity-50 disabled:hover:text-muted-foreground',
          destructive ? 'hover:text-destructive' : 'hover:text-foreground',
        )}
      >
        {run.isPending ? (
          <Loader2 className="size-3.5 animate-spin" />
        ) : (
          <Icon className="size-3.5" />
        )}
        {run.isPending ? pendingLabel : label}
      </button>
      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        windowTitle={windowTitle}
        title={title}
        description={description}
        confirmLabel={confirmLabel}
        destructive={destructive}
        onConfirm={() => run.mutate()}
      />
    </>
  )
}
