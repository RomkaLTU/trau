import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { toast } from 'sonner'

import { Markdown } from '@/components/markdown'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import {
  CHANGELOG_URL,
  RELEASES_URL,
  applyUpdate,
  canApply,
  updateQueryOptions,
  versionLabel,
  type UpdateStatus,
} from '@/lib/update'

// WhatsNewDialog shows the release the hub is behind and the one way this
// install has of moving onto it. It owns no open state of its own, so the toast
// and the sidebar badge can both drive the same dialog.
export function WhatsNewDialog({
  status,
  open,
  onOpenChange,
}: {
  status: UpdateStatus
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const queryClient = useQueryClient()
  const apply = useMutation({
    mutationFn: applyUpdate,
    onSuccess: (next) =>
      queryClient.setQueryData(updateQueryOptions.queryKey, next),
    onError: (err: Error) => toast.error(err.message),
  })
  const applying = status.applyState.state === 'running'
  const releasesUrl = status.releaseUrl || RELEASES_URL

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        aria-describedby={undefined}
        className="gap-0 overflow-hidden border-border bg-popover p-0 shadow-xl sm:max-w-lg"
      >
        <div className="flex items-center gap-3 border-b border-border px-4 py-2.5">
          <div className="flex items-center gap-1.5" aria-hidden="true">
            <span className="size-2.5 rounded-full bg-fail" />
            <span className="size-2.5 rounded-full bg-warn" />
            <span className="size-2.5 rounded-full bg-done" />
          </div>
          <span className="font-mono text-xs text-muted-foreground">
            whats-new
          </span>
        </div>

        <div className="flex flex-col gap-3 px-4 py-4">
          <DialogTitle className="font-mono text-sm font-normal text-foreground">
            What's new in {versionLabel(status.latest)}
          </DialogTitle>
          {status.latestNotes && (
            <div className="max-h-72 overflow-y-auto overscroll-contain rounded-md border border-border bg-input px-3 py-2">
              <Markdown className="text-xs">{status.latestNotes}</Markdown>
            </div>
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

        {/* Opened as a recap of the release already running, there is nothing
            to move onto, so the upgrade instructions would be a lie. */}
        {(status.updateAvailable || status.restartPending) && (
          <div className="flex flex-col gap-2 border-t border-border px-4 py-3">
            {canApply(status) ? (
              <Button
                size="sm"
                className="w-fit font-mono text-xs"
                disabled={applying || apply.isPending}
                onClick={() => apply.mutate()}
              >
                {applying ? 'Updating via Homebrew…' : 'Update now'}
              </Button>
            ) : (
              <>
                <span className="text-xs leading-relaxed text-muted-foreground">
                  {status.upgradeCommand ? (
                    <>
                      Update it with{' '}
                      <span className="font-mono text-foreground">
                        {status.upgradeCommand}
                      </span>
                      , then restart the hub.
                    </>
                  ) : (
                    <>
                      trau was not installed by a package manager it knows, so
                      it cannot update itself — releases are at{' '}
                      <a
                        href={releasesUrl}
                        target="_blank"
                        rel="noreferrer"
                        className="font-mono text-primary underline-offset-2 hover:underline"
                      >
                        {releasesUrl}
                      </a>
                      .
                    </>
                  )}
                </span>
                <Button
                  variant="outline"
                  size="sm"
                  asChild
                  className="w-fit font-mono text-xs"
                >
                  <Link
                    to="/hub"
                    hash="updates"
                    onClick={() => onOpenChange(false)}
                  >
                    Go to Hub
                  </Link>
                </Button>
              </>
            )}
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}
