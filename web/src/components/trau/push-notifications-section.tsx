import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { Send, TriangleAlert } from 'lucide-react'
import { toast } from 'sonner'

import { TerminalCard } from '@/components/trau/terminal-card'
import { Button } from '@/components/ui/button'
import { sendTestPush, usePushNotifications } from '@/lib/push'
import { cn } from '@/lib/utils'

export function PushNotificationsSection() {
  const push = usePushNotifications()
  const [error, setError] = useState<string | null>(null)

  const toggle = useMutation({
    mutationFn: (next: boolean) => (next ? push.enable() : push.disable()),
    onSuccess: () => setError(null),
    onError: (err: Error) => setError(err.message),
  })

  const test = useMutation({
    mutationFn: sendTestPush,
    onSuccess: (result) => {
      if (result.sent === 0) {
        toast.error('No browser is subscribed — turn push on first.')
        return
      }
      toast('Test notification sent — it arrives even with trau closed.')
    },
    onError: (err: Error) => toast.error(err.message),
  })

  const blocked = push.permission === 'denied'
  const busy = toggle.isPending || !push.ready

  return (
    <section id="push-notifications" className="scroll-mt-6">
      <TerminalCard title="push notifications" bodyClassName="p-0">
        <div className="flex flex-col">
          <p className="border-b border-border/60 px-4 py-2 text-xs leading-relaxed text-muted-foreground">
            Web Push hands the notification to your browser's push service, so
            the hub can reach this machine with every trau tab and window shut.
            Delivery is per browser — each one subscribes for itself.
          </p>

          {!push.supported ? (
            <p className="px-4 py-3 text-xs leading-relaxed text-muted-foreground">
              This browser has no Push API, so the hub can only notify you while
              trau is open.
            </p>
          ) : (
            <div className="flex flex-col gap-1.5 border-b border-border/60 px-4 py-3">
              <div className="flex items-center gap-3">
                <button
                  type="button"
                  role="switch"
                  aria-checked={push.subscribed}
                  aria-label="Push notifications"
                  disabled={busy || blocked}
                  onClick={() => toggle.mutate(!push.subscribed)}
                  className={cn(
                    'relative inline-flex h-5 w-9 shrink-0 items-center rounded-full border transition-colors disabled:opacity-50',
                    push.subscribed
                      ? 'border-primary bg-primary/30'
                      : 'border-border bg-input',
                  )}
                >
                  <span
                    aria-hidden="true"
                    className={cn(
                      'inline-block size-3.5 rounded-full transition-transform',
                      push.subscribed
                        ? 'translate-x-4 bg-primary'
                        : 'translate-x-0.5 bg-muted-foreground',
                    )}
                  />
                </button>
                <span className="font-mono text-sm text-foreground">
                  Push notifications (works when trau is closed)
                </span>
              </div>
              <p className="font-sans text-xs leading-relaxed text-muted-foreground">
                {blocked
                  ? 'Notifications are blocked for this site. Allow them in your browser settings, then turn this on.'
                  : 'Turning this on asks for notification permission and registers this browser with the hub.'}
              </p>
            </div>
          )}

          {error && (
            <p
              className="inline-flex items-center gap-2 border-b border-border/60 px-4 py-3 font-mono text-xs text-fail"
              role="alert"
            >
              <TriangleAlert className="size-3.5 shrink-0" aria-hidden="true" />
              {error}
            </p>
          )}

          <div className="flex flex-wrap items-center gap-2 px-4 py-3">
            <Button
              variant="outline"
              size="sm"
              className="font-mono text-xs"
              disabled={test.isPending}
              onClick={() => test.mutate()}
            >
              <Send
                className={cn('size-3.5', test.isPending && 'animate-pulse')}
                aria-hidden="true"
              />
              {test.isPending ? 'sending' : 'Send test notification'}
            </Button>
            <span className="font-mono text-[0.7rem] text-faint">
              close every trau window first to prove it
            </span>
          </div>
        </div>
      </TerminalCard>
    </section>
  )
}
