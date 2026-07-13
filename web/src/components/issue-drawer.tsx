import { useEffect, useState, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { AlertTriangle, ExternalLink, ListPlus, Pencil } from 'lucide-react'

import { InternalIssueForm } from '@/components/internal-issue-form'
import { Markdown } from '@/components/markdown'
import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetContent,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import {
  IssueFetchError,
  internalIssueQueryOptions,
  issueQueryOptions,
  type IssueComment,
} from '@/lib/issues'
import { enqueue } from '@/lib/queue'
import { cn } from '@/lib/utils'

// IssueDrawer reads one issue in place over the backlog board: the same
// store-first GET the run-once form uses, rendered as a right-side offcanvas. The
// open issue is URL state (?issue=), so it doubles as a shareable inner page and
// an in-place drawer — the caller owns the param, the drawer just reflects it.
export function IssueDrawer({
  repo,
  issueId,
  onOpenChange,
  onSelectIssue,
}: {
  repo: string
  issueId: string | null
  onOpenChange: (open: boolean) => void
  onSelectIssue: (id: string) => void
}) {
  // shownId lags issueId so the panel keeps rendering the closing issue through
  // Radix's exit animation instead of flashing empty; the keyed body resets its
  // per-issue state whenever the shown issue changes.
  const [shownId, setShownId] = useState(issueId)
  useEffect(() => {
    if (issueId !== null) setShownId(issueId)
  }, [issueId])

  return (
    <Sheet open={issueId !== null} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="w-full gap-0 p-0 sm:max-w-xl">
        {shownId !== null && (
          <IssueDrawerBody
            key={shownId}
            repo={repo}
            id={shownId}
            onSelectIssue={onSelectIssue}
          />
        )}
      </SheetContent>
    </Sheet>
  )
}

function IssueDrawerBody({
  repo,
  id,
  onSelectIssue,
}: {
  repo: string
  id: string
  onSelectIssue: (id: string) => void
}) {
  const queryClient = useQueryClient()
  const [editing, setEditing] = useState(false)

  const query = useQuery(issueQueryOptions(repo, id))
  const issue = query.data
  const internal = issue?.source === 'internal'

  const editQuery = useQuery({
    ...internalIssueQueryOptions(repo, id),
    enabled: editing && internal,
  })

  const addToQueue = useMutation({
    mutationFn: () => enqueue(repo, { id }),
    onSuccess: (res) => queryClient.setQueryData(['queue', repo], res),
  })

  if (query.isLoading) {
    return (
      <DrawerFrame id={id}>
        <p className="text-sm text-muted-foreground">Loading issue…</p>
      </DrawerFrame>
    )
  }

  if (query.error) {
    return (
      <DrawerFrame id={id}>
        <FetchError error={query.error} id={id} />
      </DrawerFrame>
    )
  }

  if (!issue) return null

  return (
    <>
      <SheetHeader className="gap-3 border-b pr-12">
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-mono text-sm font-medium text-foreground">{issue.id}</span>
          {issue.ready && (
            <span className="rounded-full border border-emerald-500/40 bg-emerald-500/5 px-2 py-0.5 text-xs text-emerald-600 dark:text-emerald-400">
              ready
            </span>
          )}
          <span className="rounded-full border px-2 py-0.5 text-xs text-muted-foreground">
            {issue.group}
          </span>
          {issue.source && (
            <span
              className={cn(
                'rounded-full px-2 py-0.5 font-mono text-xs',
                internal
                  ? 'border border-primary/40 bg-primary/5 text-primary'
                  : 'border text-muted-foreground',
              )}
            >
              {issue.source}
            </span>
          )}
        </div>
        <SheetTitle className="text-base leading-snug">{issue.title}</SheetTitle>
        {issue.parent && (
          <button
            type="button"
            onClick={() => onSelectIssue(issue.parent!)}
            className="w-fit text-xs text-muted-foreground transition-colors hover:text-foreground"
          >
            Parent ·{' '}
            <span className="font-mono underline-offset-2 hover:underline">{issue.parent}</span>
          </button>
        )}
      </SheetHeader>

      <div className="flex-1 overflow-y-auto px-4 py-4">
        {editing && internal ? (
          editQuery.data ? (
            <InternalIssueForm
              repo={repo}
              issue={editQuery.data}
              onDone={() => {
                void queryClient.invalidateQueries({ queryKey: ['issue', repo, id] })
                setEditing(false)
              }}
              onCancel={() => setEditing(false)}
            />
          ) : (
            <p className="text-sm text-muted-foreground">Loading editor…</p>
          )
        ) : (
          <>
            {issue.description.trim() ? (
              <Markdown>{issue.description}</Markdown>
            ) : (
              <p className="text-sm text-muted-foreground">No description.</p>
            )}
            <Comments comments={issue.comments} />
          </>
        )}
      </div>

      {!editing && (
        <SheetFooter className="flex-row flex-wrap items-center gap-2 border-t">
          <Button
            size="sm"
            onClick={() => addToQueue.mutate()}
            disabled={addToQueue.isPending || addToQueue.isSuccess}
          >
            <ListPlus />
            {addToQueue.isSuccess ? 'Queued' : 'Add to queue'}
          </Button>
          {internal && (
            <Button variant="outline" size="sm" onClick={() => setEditing(true)}>
              <Pencil />
              Edit
            </Button>
          )}
          {issue.url && (
            <Button variant="outline" size="sm" asChild>
              <a href={issue.url} target="_blank" rel="noreferrer">
                <ExternalLink />
                Open in {trackerName(issue.provider)}
              </a>
            </Button>
          )}
          {addToQueue.error && (
            <p className="w-full text-xs text-destructive">
              {String((addToQueue.error as Error).message)}
            </p>
          )}
        </SheetFooter>
      )}
    </>
  )
}

function DrawerFrame({ id, children }: { id: string; children: ReactNode }) {
  return (
    <>
      <SheetHeader className="border-b pr-12">
        <SheetTitle className="font-mono text-sm">{id}</SheetTitle>
      </SheetHeader>
      <div className="flex-1 overflow-y-auto px-4 py-4">{children}</div>
    </>
  )
}

function Comments({ comments }: { comments: IssueComment[] }) {
  if (comments.length === 0) return null
  return (
    <div className="mt-6 flex flex-col gap-3 border-t pt-4">
      <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        Comments · {comments.length}
      </h3>
      {comments.map((comment, i) => {
        const at = comment.created_at ? when(comment.created_at) : ''
        return (
          <div key={i} className="flex flex-col gap-1 rounded-md border bg-card px-3 py-2">
            <div className="flex items-center gap-2 text-xs text-muted-foreground">
              <span className="font-medium text-foreground">{comment.author || 'unknown'}</span>
              {at && <span className="tabular-nums">{at}</span>}
            </div>
            <Markdown>{comment.body}</Markdown>
          </div>
        )
      })}
    </div>
  )
}

function FetchError({ error, id }: { error: unknown; id: string }) {
  const kind = error instanceof IssueFetchError ? error.kind : 'error'

  if (kind === 'not-found') {
    return (
      <Notice tone="fail" title={`${id} not found`}>
        Check the ticket id and that it exists in this repo's tracker.
      </Notice>
    )
  }

  if (kind === 'no-tracker') {
    return (
      <Notice tone="warn" title="No direct tracker for this repo">
        Reading a ticket needs direct tracker credentials. Add them in{' '}
        <Link to="/settings" className="text-primary hover:underline">
          settings
        </Link>
        .
      </Notice>
    )
  }

  return (
    <p className="font-mono text-sm text-destructive">
      {error instanceof Error ? error.message : String(error)}
    </p>
  )
}

function Notice({
  tone,
  title,
  children,
}: {
  tone: 'fail' | 'warn'
  title: string
  children: ReactNode
}) {
  return (
    <div
      role="alert"
      className={cn(
        'flex items-start gap-2.5 rounded-md border px-3 py-3',
        tone === 'fail' ? 'border-fail/40 bg-fail/5' : 'border-warn/40 bg-warn/5',
      )}
    >
      <AlertTriangle
        className={cn('mt-0.5 size-3.5 shrink-0', tone === 'fail' ? 'text-fail' : 'text-warn')}
        aria-hidden="true"
      />
      <div className="flex flex-col gap-0.5">
        <p className="font-mono text-sm text-foreground">{title}</p>
        <p className="text-xs leading-relaxed text-muted-foreground">{children}</p>
      </div>
    </div>
  )
}

function trackerName(provider: string): string {
  return provider === 'jira' ? 'Jira' : 'Linear'
}

function when(ts: string): string {
  const d = new Date(ts)
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' })
}
