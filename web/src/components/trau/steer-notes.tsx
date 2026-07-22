import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Loader2, Send } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { useEventFeed } from '@/lib/events'
import {
  queueSteerNote,
  steerDeliveryModes,
  steerNotesQueryKey,
  steerNotesQueryOptions,
  steerStatusLabel,
  STEER_PLACEHOLDER,
  STEER_SETTLED_HINT,
  type SteerNote,
  type SteerStatus,
} from '@/lib/steer'

const STATUS_CLASS: Record<SteerStatus, string> = {
  pending: 'text-teal',
  delivered: 'text-done',
  expired: 'text-faint',
}

function firstLine(body: string): string {
  const [line] = body.split('\n')
  return line.length > 80 ? `${line.slice(0, 79)}…` : line
}

// No local echo: on claude phases the note echoes in the tailed transcript.
// Drop showQueued where a timeline already lists the same notes.
export function SteerComposer({
  repo,
  ticket,
  settled,
  showQueued = true,
  className,
}: {
  repo: string
  ticket: string
  settled: boolean
  showQueued?: boolean
  className?: string
}) {
  const queryClient = useQueryClient()
  const [body, setBody] = useState('')
  const { data } = useQuery(steerNotesQueryOptions(repo, ticket))

  const queue = useMutation({
    mutationFn: (text: string) => queueSteerNote(repo, ticket, text),
    onSuccess: () => {
      setBody('')
      void queryClient.invalidateQueries({
        queryKey: steerNotesQueryKey(repo, ticket),
      })
    },
  })

  const queued = showQueued
    ? (data?.notes.filter((n) => n.status === 'pending') ?? [])
    : []
  const empty = body.trim() === ''
  const send = () => {
    if (settled || empty || queue.isPending) return
    queue.mutate(body.trim())
  }

  return (
    <div className={cn('flex flex-col gap-2', className)}>
      <div
        title={settled ? STEER_SETTLED_HINT : undefined}
        className="flex items-end gap-2 rounded-lg border border-border bg-card px-3 py-2"
      >
        <textarea
          value={body}
          rows={1}
          disabled={settled}
          aria-label={`Steer ${ticket}`}
          placeholder={
            settled ? 'Steering closed — this run has settled' : STEER_PLACEHOLDER
          }
          onChange={(e) => setBody(e.target.value)}
          onKeyDown={(e) => {
            if (e.key !== 'Enter' || e.shiftKey) return
            e.preventDefault()
            send()
          }}
          className="max-h-40 flex-1 resize-none bg-transparent py-1.5 font-sans text-sm text-foreground outline-none field-sizing-content placeholder:text-muted-foreground disabled:cursor-not-allowed disabled:opacity-60"
        />
        <Button
          size="sm"
          className="font-mono"
          disabled={settled || empty || queue.isPending}
          onClick={send}
        >
          {queue.isPending ? (
            <Loader2 className="size-4 animate-spin" aria-hidden="true" />
          ) : (
            <Send className="size-4" aria-hidden="true" />
          )}
          Steer
        </Button>
      </div>

      {settled && (
        <p className="font-mono text-[0.65rem] text-muted-foreground">
          {STEER_SETTLED_HINT}
        </p>
      )}

      {queue.error && (
        <p className="font-mono text-xs text-destructive">
          {(queue.error as Error).message}
        </p>
      )}

      {queued.length > 0 && (
        <ul className="flex flex-wrap gap-2">
          {queued.map((note) => (
            <li
              key={note.id}
              className="inline-flex max-w-full items-center gap-1.5 rounded-full border border-teal/40 bg-teal/10 px-2.5 py-1 font-mono text-[0.7rem] text-teal"
            >
              <span className="cursor-block shrink-0" aria-hidden="true">
                ▍
              </span>
              <span className="truncate">queued · {firstLine(note.body)}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

export function SteerNotesTimeline({
  repo,
  ticket,
}: {
  repo: string
  ticket: string
}) {
  const { data, error } = useQuery(steerNotesQueryOptions(repo, ticket))
  const feed = useEventFeed(repo)
  const modes = useMemo(() => steerDeliveryModes(feed.events), [feed.events])

  if (error) {
    return (
      <p className="font-mono text-sm text-destructive">
        {(error as Error).message}
      </p>
    )
  }

  const notes = data?.notes ?? []
  if (notes.length === 0) {
    return (
      <p className="font-sans text-sm leading-relaxed text-muted-foreground">
        No steer notes for this run — nothing was typed while it ran.
      </p>
    )
  }

  return (
    <ul className="flex flex-col gap-3">
      {notes.map((note) => (
        <SteerNoteRow key={note.id} note={note} midPhase={modes.get(note.id)} />
      ))}
    </ul>
  )
}

function SteerNoteRow({
  note,
  midPhase,
}: {
  note: SteerNote
  midPhase?: boolean
}) {
  return (
    <li className="flex flex-col gap-1 border-b border-border/60 pb-3 last:border-0 last:pb-0">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1 font-mono text-[0.7rem]">
        <span className={STATUS_CLASS[note.status]}>
          {steerStatusLabel(note, midPhase)}
        </span>
        {note.created_at && (
          <span className="tabular-nums text-muted-foreground">
            {note.created_at}
          </span>
        )}
      </div>
      <p className="whitespace-pre-wrap text-pretty font-sans text-sm leading-relaxed text-foreground">
        {note.body}
      </p>
    </li>
  )
}
