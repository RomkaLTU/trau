import { useEffect, useReducer, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  AlertTriangle,
  ArrowLeft,
  Check,
  CheckCircle2,
  Eye,
  Loader2,
  PauseCircle,
  Pencil,
  Send,
  Sparkles,
  Trash2,
  XCircle,
  type LucideIcon,
} from 'lucide-react'

import { Markdown } from '@/components/markdown'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { StatusPill, type RunState } from '@/components/trau'
import {
  abandonGrill,
  activeSessionForIssue,
  answerGrill,
  answerText,
  applyGrill,
  diffHasChanges,
  diffLines,
  grillBanner,
  grillReducer,
  grillSessionsQueryOptions,
  grillDetailQueryOptions,
  grillStreamURL,
  isAwaitingAnswer,
  latestOutcome,
  outcomePayload,
  pendingQuestion,
  questionPayload,
  startGrillSession,
  type DiffLine,
  type GrillApplyResponse,
  type GrillApplyStep,
  type GrillBanner,
  type GrillBannerTone,
  type GrillListResponse,
  type GrillMessage,
  type GrillSession,
  type GrillState,
  type OutcomePayload,
  type QuestionPayload,
} from '@/lib/grill'
import { issueQueryOptions } from '@/lib/issues'
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
  onApplied,
}: {
  repo: string
  issueId: string
  onClose: () => void
  // onApplied fires once an outcome fully lands on the tracker, so the triage
  // inbox can auto-advance to the next unclear issue.
  onApplied?: () => void
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
    return (
      <GrillConversation
        key={session.id}
        repo={repo}
        initial={session}
        onClose={onClose}
        onApplied={onApplied}
      />
    )
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
  repo,
  initial,
  onClose,
  onApplied,
}: {
  repo: string
  initial: GrillSession
  onClose: () => void
  onApplied?: () => void
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
  const outcomeMsg = latestOutcome(messages)
  const reviewing =
    outcomeMsg !== null && (session.state === 'finished' || session.state === 'applied')

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
  const showBanner = banner !== null && banner.tone !== 'thinking' && !reviewing
  const showFooter = reviewing || showBanner || awaiting || answer.error !== null

  return (
    <PanelFrame onClose={onClose} pill={statePill(session.state)} reconnecting={status === 'error'}>
      <div className="flex-1 overflow-y-auto px-4 py-4">
        <div className="flex flex-col gap-3">
          {messages.map((m) => {
            if (pending && m.id === pending.id) return null
            if (reviewing && outcomeMsg && m.id === outcomeMsg.id) return null
            return <MessageRow key={m.id} message={m} />
          })}
          {session.state === 'running' && <ThinkingRow />}
          <div ref={bottom} />
        </div>
      </div>

      {showFooter && (
        <div className="flex flex-col gap-3 border-t p-4">
          {reviewing && outcomeMsg ? (
            <OutcomeReview
              repo={repo}
              issueId={session.issue_id ?? ''}
              session={session}
              outcome={outcomePayload(outcomeMsg)}
              onSession={(next) => dispatch({ type: 'state', session: next })}
              onApplied={onApplied}
            />
          ) : (
            <>
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
            </>
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

// OutcomeReview is the approve-then-apply gate for a finished session: the proposal
// is shown for review — a rewrite as an old→new diff the user can edit, a
// needs_split or no_change as a plain confirmation — and nothing reaches the tracker
// until Apply. A partial apply keeps the session finished and shows each step so the
// user can retry; a full apply flips the session to applied and refreshes the
// drawer's issue so it leaves the unclear set.
function OutcomeReview({
  repo,
  issueId,
  session,
  outcome,
  onSession,
  onApplied,
}: {
  repo: string
  issueId: string
  session: GrillSession
  outcome: OutcomePayload
  onSession: (session: GrillSession) => void
  onApplied?: () => void
}) {
  const queryClient = useQueryClient()
  const issue = useQuery(issueQueryOptions(repo, issueId))
  const isRewrite = outcome.disposition === 'rewrite'
  const [draft, setDraft] = useState(outcome.proposed_description ?? '')
  const [editing, setEditing] = useState(false)

  // The session's new state rides onSession (and the hub's SSE state frame), so the
  // grill list is left to go stale on its own — invalidating it here would drop the
  // panel's now-settled active session and retrigger GrillPanel's auto-start. Only
  // the issue and board are refreshed, which is what makes the issue leave the
  // unclear set once its triage labels are gone.
  const apply = useMutation({
    mutationFn: () => applyGrill(session.id, isRewrite ? draft : ''),
    onSuccess: (res) => {
      onSession(res.session)
      if (res.applied) {
        void queryClient.invalidateQueries({ queryKey: ['issue', repo, issueId] })
        void queryClient.invalidateQueries({ queryKey: ['backlog', repo] })
        onApplied?.()
      }
    },
  })

  const discard = useMutation({
    mutationFn: () => abandonGrill(session.id),
    onSuccess: onSession,
  })

  if (session.state === 'applied') {
    return <AppliedCard outcome={outcome} steps={apply.data?.steps ?? []} />
  }

  const failedSteps = apply.data && !apply.data.applied ? apply.data.steps : []
  const busy = apply.isPending || discard.isPending

  return (
    <div className="flex flex-col gap-3 rounded-lg border border-info/40 bg-info/5 p-3">
      <div className="flex items-center gap-2">
        <Badge variant="outline">{dispositionLabel(outcome.disposition)}</Badge>
        <span className="text-xs text-muted-foreground">Review before applying</span>
      </div>

      {isRewrite ? (
        <RewriteBody
          current={issue.data?.description ?? ''}
          draft={draft}
          editing={editing}
          loading={issue.isLoading}
          onChange={setDraft}
          onEdit={() => setEditing(true)}
          onPreview={() => setEditing(false)}
        />
      ) : (
        <p className="text-xs leading-relaxed text-muted-foreground">
          {outcome.disposition === 'no_change'
            ? 'No changes are needed. Close this session out — nothing is written to the tracker.'
            : 'Marks the issue needs-split and posts the summary comment. The description is left unchanged.'}
        </p>
      )}

      <SummaryPreview summary={outcome.summary} />

      {failedSteps.length > 0 && <StepList steps={failedSteps} />}

      {apply.error && <p className="text-xs text-destructive">{(apply.error as Error).message}</p>}
      {discard.error && (
        <p className="text-xs text-destructive">{(discard.error as Error).message}</p>
      )}

      <div className="flex items-center gap-2">
        <Button size="sm" onClick={() => apply.mutate()} disabled={busy}>
          {apply.isPending ? <Loader2 className="animate-spin" /> : <Check />}
          {applyLabel(outcome.disposition, apply.data)}
        </Button>
        {outcome.disposition !== 'no_change' && (
          <Button variant="ghost" size="sm" onClick={() => discard.mutate()} disabled={busy}>
            {discard.isPending ? <Loader2 className="animate-spin" /> : <Trash2 />}
            Discard
          </Button>
        )}
      </div>
    </div>
  )
}

function RewriteBody({
  current,
  draft,
  editing,
  loading,
  onChange,
  onEdit,
  onPreview,
}: {
  current: string
  draft: string
  editing: boolean
  loading: boolean
  onChange: (text: string) => void
  onEdit: () => void
  onPreview: () => void
}) {
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium text-muted-foreground">Description</span>
        {editing ? (
          <Button variant="ghost" size="sm" className="h-6 px-2 text-xs" onClick={onPreview}>
            <Eye />
            Preview diff
          </Button>
        ) : (
          <Button variant="ghost" size="sm" className="h-6 px-2 text-xs" onClick={onEdit}>
            <Pencil />
            Edit
          </Button>
        )}
      </div>
      {editing ? (
        <textarea
          value={draft}
          onChange={(e) => onChange(e.target.value)}
          rows={10}
          className="min-h-40 w-full resize-y rounded-md border bg-card px-3 py-2 font-mono text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        />
      ) : loading ? (
        <p className="text-xs text-muted-foreground">Loading the current description…</p>
      ) : (
        <DiffView current={current} next={draft} />
      )}
    </div>
  )
}

function DiffView({ current, next }: { current: string; next: string }) {
  const lines = diffLines(current, next)
  if (!diffHasChanges(lines)) {
    return (
      <p className="rounded-md border bg-card px-3 py-2 text-xs text-muted-foreground">
        No change from the current description.
      </p>
    )
  }
  return (
    <div className="max-h-72 overflow-auto rounded-md border bg-card py-1 font-mono text-xs leading-relaxed">
      {lines.map((line, i) => (
        <DiffRow key={i} line={line} />
      ))}
    </div>
  )
}

function DiffRow({ line }: { line: DiffLine }) {
  const style =
    line.op === 'insert'
      ? 'bg-done/10 text-done'
      : line.op === 'delete'
        ? 'bg-fail/10 text-fail'
        : 'text-muted-foreground'
  const sign = line.op === 'insert' ? '+' : line.op === 'delete' ? '-' : ' '
  return (
    <div className={cn('flex gap-2 px-3 whitespace-pre-wrap', style)}>
      <span aria-hidden="true" className="select-none">
        {sign}
      </span>
      <span className="flex-1 break-words">{line.text || ' '}</span>
    </div>
  )
}

function SummaryPreview({ summary }: { summary: string }) {
  const text = summary.trim()
  if (text === '') return null
  return (
    <div className="flex flex-col gap-1">
      <span className="text-xs font-medium text-muted-foreground">Summary comment</span>
      <div className="rounded-md border bg-card px-3 py-2 text-sm">
        <Markdown>{text}</Markdown>
      </div>
    </div>
  )
}

const STEP_LABELS: Record<string, string> = {
  description: 'Description',
  comment: 'Summary comment',
  labels: 'Labels',
}

function StepList({ steps }: { steps: GrillApplyStep[] }) {
  return (
    <div className="flex flex-col gap-1.5 rounded-md border bg-card px-3 py-2">
      {steps.map((step) => {
        const ok = step.status === 'ok'
        return (
          <div key={step.step} className="flex items-start gap-2 text-xs">
            {ok ? (
              <Check className="mt-0.5 size-3.5 shrink-0 text-done" aria-hidden="true" />
            ) : (
              <XCircle className="mt-0.5 size-3.5 shrink-0 text-fail" aria-hidden="true" />
            )}
            <div className="flex flex-col gap-0.5">
              <span className={ok ? 'text-foreground' : 'text-fail'}>
                {STEP_LABELS[step.step] ?? step.step}
              </span>
              {step.error && <span className="text-muted-foreground">{step.error}</span>}
            </div>
          </div>
        )
      })}
    </div>
  )
}

function AppliedCard({ outcome, steps }: { outcome: OutcomePayload; steps: GrillApplyStep[] }) {
  return (
    <div className="flex flex-col gap-3 rounded-lg border border-done/40 bg-done/5 p-3">
      <div className="flex items-center gap-2">
        <CheckCircle2 className="size-4 shrink-0 text-done" aria-hidden="true" />
        <p className="text-sm font-medium">Applied</p>
        <Badge variant="outline">{dispositionLabel(outcome.disposition)}</Badge>
      </div>
      <p className="text-xs leading-relaxed text-muted-foreground">
        {outcome.disposition === 'no_change'
          ? 'Session closed out — nothing was written to the tracker.'
          : 'The outcome was written to the tracker. This issue is cleared.'}
      </p>
      {steps.length > 0 && <StepList steps={steps} />}
    </div>
  )
}

function applyLabel(disposition: string, result?: GrillApplyResponse): string {
  if (result && !result.applied) return 'Retry'
  if (disposition === 'no_change') return 'Close out'
  return 'Apply'
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
