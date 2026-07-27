import { useEffect, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Loader2, RotateCw } from 'lucide-react'

import { Eyebrow } from '@/components/trau/eyebrow'
import { TerminalCard } from '@/components/trau/terminal-card'
import { Button } from '@/components/ui/button'
import {
  retryHubNow,
  secondsUntil,
  startHubWatch,
  useHubConnectivity,
  type HubConnectivity,
} from '@/lib/connectivity'
import { cn } from '@/lib/utils'

// Covers the app rather than replacing it: a hub that dies mid-session must not
// take unsaved text in an open drawer or interview down with it, and the restart
// flow's successor watch keeps running underneath.
export function HubGate({ onRecover }: { onRecover: () => void }) {
  const hub = useHubConnectivity()
  const queryClient = useQueryClient()
  const wasDown = useRef(false)

  useEffect(startHubWatch, [])

  // The SSE client reconnects on its own timer; the queries and the route
  // matches have to be told. A loader that rejected while the hub was gone
  // leaves its route parked on the error boundary until the match is reset.
  useEffect(() => {
    if (hub.status === 'unreachable') {
      wasDown.current = true
      return
    }
    if (!wasDown.current) return
    wasDown.current = false
    void queryClient.invalidateQueries()
    onRecover()
  }, [hub.status, queryClient, onRecover])

  if (hub.status === 'online') return null
  return <HubUnreachable hub={hub} />
}

function HubUnreachable({ hub }: { hub: HubConnectivity }) {
  const seconds = useRetryCountdown(hub.retryAt)

  // An open dialog or sheet switches off pointer events outside itself, which
  // reaches the gate too and would leave Retry now unclickable. It also
  // dismisses itself on any pointerdown that reaches the document, so the
  // gate's own clicks stop here rather than closing a half-written drawer.
  return (
    <div
      role="alert"
      onPointerDown={(event) => event.stopPropagation()}
      className="furrow-grid pointer-events-auto fixed inset-0 z-[120] flex items-center justify-center bg-background p-6"
    >
      <div
        className="hero-glow pointer-events-none absolute inset-0"
        aria-hidden="true"
      />
      <TerminalCard
        title="trau serve — unreachable"
        className="relative w-full max-w-md"
      >
        <div className="flex flex-col gap-4">
          <Eyebrow glyph="warn">hub</Eyebrow>
          <p className="font-mono text-3xl font-medium tracking-tight text-foreground">
            trau
            <span className="cursor-block text-primary">▍</span>
          </p>
          <p className="text-sm leading-relaxed text-muted-foreground">
            Hub unreachable — start{' '}
            <span className="font-mono text-foreground">trau serve</span> and
            this page picks it back up on its own.
          </p>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <span className="inline-flex items-center gap-2 font-mono text-xs text-muted-foreground">
              <Loader2
                className={cn(
                  'size-3.5',
                  hub.probing ? 'animate-spin' : 'opacity-40',
                )}
                aria-hidden="true"
              />
              {hub.probing ? 'reconnecting…' : `retrying in ${seconds}s`}
            </span>
            <Button
              variant="outline"
              size="sm"
              className="font-mono text-xs"
              disabled={hub.probing}
              onClick={retryHubNow}
            >
              <RotateCw className="size-3.5" aria-hidden="true" />
              Retry now
            </Button>
          </div>
        </div>
      </TerminalCard>
    </div>
  )
}

function useRetryCountdown(retryAt: number): number {
  const [seconds, setSeconds] = useState(() => secondsUntil(retryAt, Date.now()))

  useEffect(() => {
    const tick = () => setSeconds(secondsUntil(retryAt, Date.now()))
    tick()
    const id = setInterval(tick, 250)
    return () => clearInterval(id)
  }, [retryAt])

  return seconds
}
