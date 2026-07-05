import { useEffect, useState } from 'react'

import { Button } from '@/components/ui/button'
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
} from '@/components/ui/alert-dialog'

export function ForceResetDialog({
  open,
  onOpenChange,
  ticket,
  onConfirm,
  pending,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  ticket: string
  onConfirm: () => void
  pending: boolean
}) {
  const [value, setValue] = useState('')
  useEffect(() => {
    if (!open) setValue('')
  }, [open])
  const matches = value.trim() === ticket

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent className="gap-0 overflow-hidden border-border bg-popover p-0 shadow-xl sm:max-w-sm">
        <div className="flex items-center gap-3 border-b border-border px-4 py-2.5">
          <div className="flex items-center gap-1.5" aria-hidden="true">
            <span className="size-2.5 rounded-full bg-fail" />
            <span className="size-2.5 rounded-full bg-warn" />
            <span className="size-2.5 rounded-full bg-done" />
          </div>
          <span className="font-mono text-xs text-muted-foreground">force-confirm</span>
        </div>
        <div className="flex flex-col gap-2 p-4">
          <h3 className="font-mono text-sm text-foreground">Reset {ticket}?</h3>
          <p className="font-sans text-sm leading-relaxed text-muted-foreground">
            {ticket} is merged. Resetting drops its shipped branch and re-queues it on the
            tracker — this cannot be undone. Type the ticket id to confirm.
          </p>
          <input
            type="text"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            placeholder={ticket}
            aria-label="Type the ticket id to confirm"
            autoComplete="off"
            spellCheck={false}
            className="mt-2 w-full rounded-md border border-border bg-input px-2.5 py-1.5 font-mono text-sm text-foreground placeholder:text-faint focus-visible:border-ring focus-visible:outline-none"
          />
          <div className="mt-3 flex justify-end gap-2">
            <AlertDialogCancel className="h-8 gap-1.5 px-3 font-mono text-sm">
              Cancel
            </AlertDialogCancel>
            <Button
              variant="destructive"
              size="sm"
              className="font-mono"
              disabled={!matches || pending}
              onClick={onConfirm}
            >
              {pending ? 'Resetting…' : 'Reset ticket'}
            </Button>
          </div>
        </div>
      </AlertDialogContent>
    </AlertDialog>
  )
}
