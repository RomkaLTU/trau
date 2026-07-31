import type { ReactElement, ReactNode } from 'react'

import { cn } from '@/lib/utils'
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

export function ConfirmDialog({
  trigger,
  open,
  onOpenChange,
  windowTitle = 'confirm',
  title,
  description,
  confirmLabel = 'Confirm',
  cancelLabel = 'Cancel',
  onConfirm,
  confirmDisabled = false,
  destructive = false,
}: {
  trigger?: ReactElement
  open?: boolean
  onOpenChange?: (open: boolean) => void
  windowTitle?: string
  title: ReactNode
  description?: ReactNode
  confirmLabel?: string
  cancelLabel?: string
  onConfirm: () => void
  confirmDisabled?: boolean
  destructive?: boolean
}) {
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      {trigger ? (
        <AlertDialogTrigger asChild>{trigger}</AlertDialogTrigger>
      ) : null}
      {/* Only the description scrolls: a destructive confirm has to keep saying what
          it is confirming, and keep its buttons reachable, however long that runs. */}
      <AlertDialogContent className="flex max-h-[calc(100dvh-2rem)] flex-col gap-0 overflow-hidden border-border bg-popover p-0 shadow-xl sm:max-w-sm">
        <div className="flex shrink-0 items-center gap-3 border-b border-border px-4 py-2.5">
          <div className="flex items-center gap-1.5" aria-hidden="true">
            <span className="size-2.5 rounded-full bg-fail" />
            <span className="size-2.5 rounded-full bg-warn" />
            <span className="size-2.5 rounded-full bg-done" />
          </div>
          <span className="font-mono text-xs text-muted-foreground">
            {windowTitle}
          </span>
        </div>
        <AlertDialogHeader className="shrink-0 px-4 pb-2 pt-4 text-left">
          <AlertDialogTitle className="font-mono text-sm font-normal text-foreground">
            {title}
          </AlertDialogTitle>
        </AlertDialogHeader>
        {description ? (
          <div
            data-slot="confirm-dialog-body"
            className="min-h-0 max-h-[60dvh] flex-1 overflow-y-auto overscroll-contain px-4 pb-2"
          >
            <AlertDialogDescription className="font-sans text-sm leading-relaxed text-muted-foreground">
              {description}
            </AlertDialogDescription>
          </div>
        ) : null}
        <AlertDialogFooter className="shrink-0 border-t border-border px-4 pb-4 pt-2">
          <AlertDialogCancel className="h-8 gap-1.5 px-3 font-mono text-sm">
            {cancelLabel}
          </AlertDialogCancel>
          <AlertDialogAction
            onClick={onConfirm}
            disabled={confirmDisabled}
            className={cn(
              'h-8 gap-1.5 px-3 font-mono text-sm',
              destructive &&
                'bg-destructive text-white hover:bg-destructive/90',
            )}
          >
            {confirmLabel}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
