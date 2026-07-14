import { useEffect, useReducer, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  AlertTriangle,
  ArrowLeft,
  CheckCircle2,
  Loader2,
  PauseCircle,
  Send,
  Sparkles,
  XCircle,
  type LucideIcon,
} from 'lucide-react'

import { Markdown } from '@/components/markdown'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { StatusPill, type RunState } from '@/components/trau'
import {
  activeSessionForIssue,
  answerGrill,
  answerText,
  grillBanner,
  grillReducer,
  grillSessionsQueryOptions,
  grillDetailQueryOptions,
  grillStreamURL,
  isAwaitingAnswer,
  outcomePayload,
  pendingQuestion,
  questionPayload,
  startGrillSession,
  type GrillBanner,
  type GrillBannerTone,
  type GrillListResponse,
  type GrillMessage,
  type GrillSession,
  type GrillState,
  type OutcomePayload,
  type QuestionPayload,
} from '@/lib/grill'
import { streamSSE } from '@/lib/sse'
import { cn } from '@/lib/utils'

// GrillPanel is the chat surface for one issue's grilling session, mounted in the
// backlog drawer. It reopens the issue's live session if one exists, otherwise
// starts one — the whole conversation is server-side, so closing and reopening the
// drawer restores the thread and any pending question.
export function GrillPanel({
  repo,
  issueId,
  onClose,
}: {
  repo: string
  issueId: string
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  const list = useQuery(grillSessionsQueryOptions(repo))
  const active = activeSessionForIssue(list.data?.sessions, issueId)
  const started = useRef(false)

  const create = useMutation({
    mutationFn: () => startGrillSession(repo, issueId),
    onSuccess: (sess) => {
      queryClient.setQueryData<GrillListResponse>(['grill', repo], (prev) =>
        prev
          ? { ...prev, sessions: [sess, ...prev.sessions.filter((s) => s.id !== sess.id)] }
          : { repo, sessions: [sess] },
      )
    },
    onError: () => void queryClient.invalidateQueries({ queryKey: ['grill', repo] }),
  })

  useEffect(() => {
    if (!list.isSuccess || active || started.current) return
    started.current = true
    create.mutate()
  }, [list.isSuccess, active])

  const session = active ?? create.data

  if (session) {
    return <GrillConversation key={session.id} initial={session} onClose={onClose} />
  }

  return (
    <PanelFrame onClose={onClose}>
      <div className="flex flex-1 items-center justify-center px-4">
        {list.error ? (
          <ErrorNote message={(list.error as Error).message} />
        ) : create.error && !active ? (
          <div className="flex flex-col items-center gap-3">
            <ErrorNote message={(create.error as Error).message} />
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                started.current = true
                create.mutate()
              }}
            >
              Try again
            </Button>
          </div>
        ) : (
          <p className="inline-flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="size-4 animate-spin" />
            {create.isPending ? 'Starting grilling session…' : 'Loading…'}
          </p>
        )}
      </div>
    </PanelFrame>
  )
}

type StreamStatus = 'connecting' | 'live' | 'error'

function GrillConversation({
  initial,
  onClose,
}: {
  initial: GrillSession
  onClose: () => void
}) {
  const detail = useQuery(grillDetailQueryOptions(initial.id))
  const [state, dispatch] = useReducer(grillReducer, undefined, () => ({
    session: initial,
    live: false,
    messages: [],
  }))
  const [status, setStatus] = useState<StreamStatus>('connecting')
  const bottom = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (detail.data) dispatch({ type: 'hydrate', detail: detail.data })
  }, [detail.data])

  useEffect(() => {
    setStatus('connecting')
    const close = streamSSE(grillStreamURL(initial.id), {
      onOpen: () => setStatus('live'),
      onError: () => setStatus('error'),
      onMessage: ({ event, data }) => {
        let parsed: unknown
        try {
          parsed = JSON.parse(data)
        } catch {
          return
        }
        if (event === 'state') dispatch({ type: 'state', session: parsed as GrillSession })
        else if (event === 'message') dispatch({ type: 'message', message: parsed as GrillMessage })
      },
    })
    return () => close()
  }, [initial.id])

  const { session, messages } = state
  const pending = pendingQuestion(messages)

  const answer = useMutation({
    mutationFn: (text: string) => answerGrill(session.id, text),
    onSuccess: (res) => {
      dispatch({ type: 'message', message: res.message })
      dispatch({ type: 'state', session: res.session })
    },
  })

  useEffect(() => {
    bottom.current?.scrollIntoView({ block: 'end' })
  }, [messages, session.state, answer.isPending])

  const awaiting = isAwaitingAnswer(session.state)
  const banner = grillBanner(session)
  const showBanner = banner !== null && banner.tone !== 'thinking'
  const showFooter = showBanner || awaiting || answer.error !== null

  return (
    <PanelFrame onClose={onClose} pill={statePill(session.state)} reconnecting={status === 'error'}>
      <div className="flex-1 overflow-y-auto px-4 py-4">
        <div className="flex flex-col gap-3">
          {messages.map((m) => {
            if (pending && m.id === pending.id) return null
            return <MessageRow key={m.id} message={m} />
          })}
          {session.state === 'running' && <ThinkingRow />}
          <div ref={bottom} />
        </div>
      </div>

      {showFooter && (
        <div className="flex flex-col gap-3 border-t p-4">
          {showBanner && <BannerRow banner={banner} />}
          {awaiting &&
            (pending ? (
              <QuestionCard
                question={questionPayload(pending)}
                disabled={answer.isPending}
                onAnswer={(text) => answer.mutate(text)}
              />
            ) : (
              <Composer
                placeholder="Reply to resume…"
                disabled={answer.isPending}
                submitting={answer.isPending}
                onSend={(text) => answer.mutate(text)}
                defaultValue={session.state === 'stalled' ? lastAnswer(messages) : ''}
              />
            ))}
          {answer.error && (
            <p className="text-xs text-destructive">{(answer.error as Error).message}</p>
          )}
        </div>
      )}
    </PanelFrame>
  )
}

function PanelFrame({
  onClose,
  pill,
  reconnecting,
  children,
}: {
  onClose: () => void
  pill?: { state: RunState; label: string }
  reconnecting?: boolean
  children: React.ReactNode
}) {
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex items-center gap-2 border-b px-4 py-2.5">
        <Button variant="ghost" size="sm" onClick={onClose} className="-ml-2">
          <ArrowLeft />
          Back
        </Button>
        <div className="flex-1" />
        {reconnecting && (
          <span className="inline-flex items-center gap-1 text-xs text-warn">
            <span aria-hidden="true">⚠</span>
            reconnecting…
          </span>
        )}
        {pill && <StatusPill state={pill.state} label={pill.label} />}
      </div>
      {children}
    </div>
  )
}

function MessageRow({ message }: { message: GrillMessage }) {
  switch (message.kind) {
    case 'question':
      return <Bubble role="agent">{questionPayload(message).text}</Bubble>
    case 'answer':
      return <Bubble role="user">{answerText(message)}</Bubble>
    case 'outcome':
      return <OutcomeProposal outcome={outcomePayload(message)} />
    default:
      return null
  }
}

function Bubble({ role, children }: { role: 'agent' | 'user'; children: React.ReactNode }) {
  const user = role === 'user'
  return (
    <div className={cn('flex', user ? 'justify-end' : 'justify-start')}>
      <div
        className={cn(
          'max-w-[85%] whitespace-pre-wrap rounded-2xl px-3 py-2 text-sm',
          user
            ? 'rounded-br-sm bg-primary text-primary-foreground'
            : 'rounded-bl-sm bg-muted text-foreground',
        )}
      >
        {children}
      </div>
    </div>
  )
}

function ThinkingRow() {
  return (
    <div className="flex justify-start">
      <span className="inline-flex items-center gap-2 rounded-2xl rounded-bl-sm bg-muted px-3 py-2 text-sm text-muted-foreground">
        <Loader2 className="size-3.5 animate-spin" />
        Thinking…
      </span>
    </div>
  )
}

function QuestionCard({
  question,
  disabled,
  onAnswer,
}: {
  question: QuestionPayload
  disabled: boolean
  onAnswer: (text: string) => void
}) {
  return (
    <div className="flex flex-col gap-3 rounded-lg border bg-card p-3">
      <p className="whitespace-pre-wrap text-sm text-foreground">{question.text}</p>
      {question.options.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {question.options.map((opt) => (
            <Button
              key={opt}
              variant="outline"
              size="sm"
              disabled={disabled}
              onClick={() => onAnswer(opt)}
            >
              {opt}
            </Button>
          ))}
        </div>
      )}
      {question.allow_free_text && (
        <Composer
          placeholder={question.options.length > 0 ? 'Or type your own answer…' : 'Type your answer…'}
          disabled={disabled}
          submitting={disabled}
          onSend={onAnswer}
        />
      )}
    </div>
  )
}

function Composer({
  placeholder,
  disabled,
  submitting,
  onSend,
  defaultValue = '',
}: {
  placeholder: string
  disabled: boolean
  submitting: boolean
  onSend: (text: string) => void
  defaultValue?: string
}) {
  const [text, setText] = useState(defaultValue)
  const send = () => {
    const trimmed = text.trim()
    if (trimmed === '' || disabled) return
    onSend(trimmed)
    setText('')
  }
  return (
    <div className="flex items-end gap-2">
      <textarea
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault()
            send()
          }
        }}
        placeholder={placeholder}
        rows={1}
        disabled={disabled}
        className="max-h-32 min-h-9 flex-1 resize-none rounded-md border bg-transparent px-3 py-2 text-sm outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-50"
      />
      <Button size="sm" onClick={send} disabled={disabled || text.trim() === ''}>
        {submitting ? <Loader2 className="animate-spin" /> : <Send />}
        Send
      </Button>
    </div>
  )
}

function OutcomeProposal({ outcome }: { outcome: OutcomePayload }) {
  return (
    <div className="flex flex-col gap-2 rounded-lg border border-info/40 bg-info/5 p-3">
      <div className="flex items-center gap-2">
        <Badge variant="outline">{dispositionLabel(outcome.disposition)}</Badge>
        <span className="text-xs text-muted-foreground">Proposed outcome</span>
      </div>
      {outcome.summary && <p className="whitespace-pre-wrap text-sm text-foreground">{outcome.summary}</p>}
      {outcome.proposed_description && (
        <details className="text-sm">
          <summary className="cursor-pointer text-xs text-muted-foreground">
            Proposed description
          </summary>
          <div className="mt-2 rounded-md border bg-card px-3 py-2">
            <Markdown>{outcome.proposed_description}</Markdown>
          </div>
        </details>
      )}
    </div>
  )
}

const BANNER_STYLES: Record<GrillBannerTone, { className: string; icon: LucideIcon; spin?: boolean }> = {
  thinking: { className: 'border-teal/40 bg-teal/5 text-foreground', icon: Loader2, spin: true },
  parked: { className: 'border-border bg-muted/40 text-foreground', icon: PauseCircle },
  stalled: { className: 'border-warn/40 bg-warn/5 text-foreground', icon: AlertTriangle },
  finished: { className: 'border-info/40 bg-info/5 text-foreground', icon: Sparkles },
  applied: { className: 'border-done/40 bg-done/5 text-foreground', icon: CheckCircle2 },
  ended: { className: 'border-border bg-muted/40 text-muted-foreground', icon: XCircle },
}

function BannerRow({ banner }: { banner: GrillBanner }) {
  const style = BANNER_STYLES[banner.tone]
  const Icon = style.icon
  return (
    <div className={cn('flex items-start gap-2.5 rounded-md border px-3 py-2.5', style.className)}>
      <Icon className={cn('mt-0.5 size-4 shrink-0', style.spin && 'animate-spin')} aria-hidden="true" />
      <div className="flex flex-col gap-0.5">
        <p className="text-sm font-medium">{banner.headline}</p>
        {banner.hint && <p className="text-xs leading-relaxed text-muted-foreground">{banner.hint}</p>}
      </div>
    </div>
  )
}

function ErrorNote({ message }: { message: string }) {
  return (
    <div className="flex items-start gap-2.5 rounded-md border border-fail/40 bg-fail/5 px-3 py-3">
      <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-fail" aria-hidden="true" />
      <p className="text-xs leading-relaxed text-muted-foreground">{message}</p>
    </div>
  )
}

const STATE_PILLS: Record<GrillState, { state: RunState; label: string }> = {
  running: { state: 'active', label: 'thinking' },
  waiting: { state: 'info', label: 'your turn' },
  parked: { state: 'todo', label: 'parked' },
  stalled: { state: 'warn', label: 'stalled' },
  finished: { state: 'verify', label: 'proposal ready' },
  applied: { state: 'success', label: 'applied' },
  abandoned: { state: 'todo', label: 'ended' },
}

function statePill(state: GrillState): { state: RunState; label: string } {
  return STATE_PILLS[state]
}

function dispositionLabel(disposition: string): string {
  switch (disposition) {
    case 'rewrite':
      return 'Rewrite'
    case 'split':
      return 'Split into epic'
    case 'needs_split':
      return 'Needs split'
    case 'create':
      return 'Create'
    case 'no_change':
      return 'No change'
    default:
      return disposition || 'Outcome'
  }
}

function lastAnswer(messages: GrillMessage[]): string {
  for (let i = messages.length - 1; i >= 0; i--) {
    if (messages[i].kind === 'answer') return answerText(messages[i])
  }
  return ''
}
