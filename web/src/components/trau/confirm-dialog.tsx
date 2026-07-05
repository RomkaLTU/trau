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
  destructive?: boolean
}) {
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      {trigger ? (
        <AlertDialogTrigger asChild>{trigger}</AlertDialogTrigger>
      ) : null}
      <AlertDialogContent className="gap-0 overflow-hidden border-border bg-popover p-0 shadow-xl sm:max-w-sm">
        <div className="flex items-center gap-3 border-b border-border px-4 py-2.5">
          <div className="flex items-center gap-1.5" aria-hidden="true">
            <span className="size-2.5 rounded-full bg-fail" />
            <span className="size-2.5 rounded-full bg-warn" />
            <span className="size-2.5 rounded-full bg-done" />
          </div>
          <span className="font-mono text-xs text-muted-foreground">
            {windowTitle}
          </span>
        </div>
        <div className="flex flex-col gap-2 p-4">
          <AlertDialogHeader className="gap-2 text-left">
            <AlertDialogTitle className="font-mono text-sm font-normal text-foreground">
              {title}
            </AlertDialogTitle>
            {description ? (
              <AlertDialogDescription className="font-sans text-sm leading-relaxed text-muted-foreground">
                {description}
              </AlertDialogDescription>
            ) : null}
          </AlertDialogHeader>
          <AlertDialogFooter className="mt-2">
            <AlertDialogCancel className="h-8 gap-1.5 px-3 font-mono text-sm">
              {cancelLabel}
            </AlertDialogCancel>
            <AlertDialogAction
              onClick={onConfirm}
              className={cn(
                'h-8 gap-1.5 px-3 font-mono text-sm',
                destructive &&
                  'bg-destructive text-white hover:bg-destructive/90',
              )}
            >
              {confirmLabel}
            </AlertDialogAction>
          </AlertDialogFooter>
        </div>
      </AlertDialogContent>
    </AlertDialog>
  )
}
