import { useEffect, useRef, useState } from 'react'
import { Terminal as Xterm } from '@xterm/xterm'
import { SquareTerminal } from 'lucide-react'
import '@xterm/xterm/css/xterm.css'

import { cn } from '@/lib/utils'
import {
  decodeChunk,
  transcriptStreamURL,
  type TranscriptMeta,
  type TranscriptStatus,
} from '@/lib/transcripts'

const THEME = {
  background: '#0a0a0a',
  foreground: '#e4e4e7',
  cursor: '#0a0a0a',
}

// Terminal renders a repo's live agent transcript in an embedded xterm.js. It
// holds one EventSource for the whole component lifetime — a single connection
// per selected repo, so switching phases never leaks connections into the
// browser's per-origin cap. The stream sizes the emulator from the meta frame's
// recorded PTY dimensions and clears it on a reset (a new phase or an in-place
// truncation) before the fresh bytes land.
export function Terminal({
  repo,
  id,
  className,
}: {
  repo: string
  id?: string
  className?: string
}) {
  const holder = useRef<HTMLDivElement>(null)
  const termRef = useRef<Xterm | null>(null)
  const followedID = useRef('')
  const [status, setStatus] = useState<TranscriptStatus>('connecting')

  useEffect(() => {
    if (!holder.current) return
    const term = new Xterm({
      convertEol: false,
      cursorBlink: false,
      disableStdin: true,
      fontSize: 12,
      fontFamily:
        '"Geist Mono Variable", ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace',
      scrollback: 5000,
      theme: THEME,
    })
    term.open(holder.current)
    termRef.current = term
    return () => {
      term.dispose()
      termRef.current = null
    }
  }, [])

  useEffect(() => {
    const term = termRef.current
    if (!term || !repo) return

    followedID.current = ''
    term.reset()
    setStatus('connecting')

    const source = new EventSource(transcriptStreamURL(repo, id))
    source.onopen = () => setStatus('live')
    source.onerror = () => setStatus('error')
    source.addEventListener('meta', (e) => {
      let meta: TranscriptMeta
      try {
        meta = JSON.parse((e as MessageEvent).data)
      } catch {
        return
      }
      if (meta.id !== followedID.current) {
        term.reset()
        followedID.current = meta.id
      }
      term.resize(Math.max(1, meta.cols), Math.max(1, meta.rows))
    })
    source.addEventListener('reset', () => term.reset())
    source.onmessage = (e) => term.write(decodeChunk(e.data))

    return () => source.close()
  }, [repo, id])

  return (
    <div className={cn('overflow-hidden rounded-lg border bg-card', className)}>
      <div className="flex items-center justify-between border-b px-3 py-2">
        <span className="flex items-center gap-2 text-sm font-medium">
          <SquareTerminal className="size-4 text-muted-foreground" />
          {id ?? 'newest'}
        </span>
        <StatusDot status={status} />
      </div>
      <div
        ref={holder}
        className="overflow-auto bg-[#0a0a0a] p-2"
        style={{ height: '32rem' }}
      />
    </div>
  )
}

function StatusDot({ status }: { status: TranscriptStatus }) {
  const meta: Record<TranscriptStatus, { label: string; dot: string }> = {
    connecting: { label: 'connecting', dot: 'bg-amber-500' },
    live: { label: 'live', dot: 'bg-emerald-500' },
    error: { label: 'reconnecting', dot: 'bg-destructive' },
  }
  const { label, dot } = meta[status]
  return (
    <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
      <span
        className={cn(
          'size-1.5 rounded-full',
          dot,
          status === 'live' && 'animate-pulse',
        )}
      />
      {label}
    </span>
  )
}
