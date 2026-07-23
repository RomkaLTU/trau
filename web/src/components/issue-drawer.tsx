import { useEffect, useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  AlertTriangle,
  Archive,
  ArchiveRestore,
  ExternalLink,
  Flame,
  ListPlus,
  Pencil,
} from "lucide-react";
import { toast } from "sonner";

import { ArchiveToast } from "@/components/archive-toast";
import {
  AssigneeDisplay,
  AssigneePicker,
} from "@/components/trau/assignee-picker";
import { DeleteIssueDialog } from "@/components/delete-issue-dialog";
import { InternalIssueForm } from "@/components/internal-issue-form";
import { IssueAttachments } from "@/components/issue-attachments";
import { Markdown, type MarkdownUrlMap } from "@/components/markdown";
import { Button } from "@/components/ui/button";
import {
  Sheet,
  SheetContent,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { type Assignee } from "@/lib/assignee";
import { assignIssue, publishAssignment } from "@/lib/assignees";
import { useIssueAttachments } from "@/lib/attachments";
import {
  IssueFetchError,
  internalIssueQueryOptions,
  issueQueryOptions,
  type IssueComment,
} from "@/lib/issues";
import { archiveToastMessage, useArchiveIssue } from "@/lib/archive";
import {
  activeSessionForIssue,
  grillSessionsQueryOptions,
  isGrillable,
} from "@/lib/grill";
import {
  enqueueFresh,
  publishQueue,
  queueActiveIds,
  queueQueryOptions,
} from "@/lib/queue";
import { cn } from "@/lib/utils";

// IssueDrawer reads one issue in place over the backlog board: the same
// store-first GET the add-ticket confirm uses, rendered as a right-side offcanvas. The
// open issue is URL state (?issue=), so it doubles as a shareable inner page and
// an in-place drawer — the caller owns the param, the drawer just reflects it.
export function IssueDrawer({
  repo,
  issueId,
  onOpenChange,
  onSelectIssue,
}: {
  repo: string;
  issueId: string | null;
  onOpenChange: (open: boolean) => void;
  onSelectIssue: (id: string) => void;
}) {
  // shownId lags issueId so the panel keeps rendering the closing issue through
  // Radix's exit animation instead of flashing empty; the keyed body resets its
  // per-issue state whenever the shown issue changes.
  const [shownId, setShownId] = useState(issueId);
  useEffect(() => {
    if (issueId !== null) setShownId(issueId);
  }, [issueId]);

  return (
    <Sheet open={issueId !== null} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="w-full gap-0 p-0 sm:max-w-xl">
        {shownId !== null && (
          <IssueDrawerBody
            key={shownId}
            repo={repo}
            id={shownId}
            onClose={() => onOpenChange(false)}
            onSelectIssue={onSelectIssue}
          />
        )}
      </SheetContent>
    </Sheet>
  );
}

function IssueDrawerBody({
  repo,
  id,
  onClose,
  onSelectIssue,
}: {
  repo: string;
  id: string;
  onClose: () => void;
  onSelectIssue: (id: string) => void;
}) {
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);

  const query = useQuery(issueQueryOptions(repo, id));
  const issue = query.data;
  const { attachments, urlMap } = useIssueAttachments(repo, id);
  const internal = issue?.source === "internal";
  const grillable = !!issue && isGrillable(issue.labels);

  const grillSessions = useQuery({
    ...grillSessionsQueryOptions(repo),
    enabled: grillable,
  });
  const activeGrill = activeSessionForIssue(grillSessions.data?.sessions, id);

  const editQuery = useQuery({
    ...internalIssueQueryOptions(repo, id),
    enabled: editing && internal,
  });

  const queue = useQuery(queueQueryOptions(repo));
  const inQueue = queueActiveIds(queue.data?.items ?? []).has(id);

  const addToQueue = useMutation({
    mutationFn: () => enqueueFresh(repo, { id }),
    onSuccess: (res) => publishQueue(queryClient, repo, res),
  });

  const assign = useMutation({
    mutationFn: (next: Assignee | null) => assignIssue(repo, id, next),
    onSuccess: (updated) => publishAssignment(queryClient, repo, updated),
    onError: (err) => toast.error(err.message),
  });

  const [archiveNote, setArchiveNote] = useState<string | null>(null);
  useEffect(() => {
    if (!archiveNote) return;
    const t = setTimeout(() => setArchiveNote(null), 6000);
    return () => clearTimeout(t);
  }, [archiveNote]);
  const archive = useArchiveIssue(repo, (result, vars) =>
    setArchiveNote(
      archiveToastMessage(vars.id, vars.archived, result.queue_removed),
    ),
  );

  if (query.isLoading) {
    return (
      <DrawerFrame id={id}>
        <p className="text-sm text-muted-foreground">Loading issue…</p>
      </DrawerFrame>
    );
  }

  if (query.error) {
    return (
      <DrawerFrame id={id}>
        <FetchError error={query.error} id={id} />
      </DrawerFrame>
    );
  }

  if (!issue) return null;

  return (
    <>
      <SheetHeader className="gap-3 border-b pr-12">
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-mono text-sm font-medium text-foreground">
            {issue.id}
          </span>
          {issue.ready && (
            <span className="rounded-full border border-emerald-500/40 bg-emerald-500/5 px-2 py-0.5 text-xs text-emerald-600 dark:text-emerald-400">
              ready
            </span>
          )}
          {inQueue && (
            <span className="rounded-full border border-sky-500/40 bg-sky-500/5 px-2 py-0.5 text-xs text-sky-600 dark:text-sky-400">
              queued
            </span>
          )}
          {issue.archived && (
            <span className="inline-flex items-center gap-1 rounded-full border border-amber-500/40 bg-amber-500/5 px-2 py-0.5 text-xs text-amber-600 dark:text-amber-400">
              <Archive className="size-3" aria-hidden />
              archived
            </span>
          )}
          <span className="rounded-full border px-2 py-0.5 text-xs text-muted-foreground">
            {issue.group}
          </span>
          {issue.source && (
            <span
              className={cn(
                "rounded-full px-2 py-0.5 font-mono text-xs",
                internal
                  ? "border border-primary/40 bg-primary/5 text-primary"
                  : "border text-muted-foreground",
              )}
            >
              {issue.source}
            </span>
          )}
        </div>
        <SheetTitle className="text-base leading-snug">
          {issue.title}
        </SheetTitle>
        {issue.parent && (
          <button
            type="button"
            onClick={() => onSelectIssue(issue.parent!)}
            className="w-fit text-xs text-muted-foreground transition-colors hover:text-foreground"
          >
            Parent ·{" "}
            <span className="font-mono underline-offset-2 hover:underline">
              {issue.parent}
            </span>
          </button>
        )}
        {issue.blockers && issue.blockers.length > 0 && (
          <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
            <span>{issue.blocked ? "Blocked by" : "Was blocked by"} ·</span>
            {issue.blockers.map((blocker) => (
              <button
                key={blocker}
                type="button"
                onClick={() => onSelectIssue(blocker)}
                className="font-mono underline-offset-2 transition-colors hover:text-foreground hover:underline"
              >
                {blocker}
              </button>
            ))}
          </div>
        )}
        <div className="flex items-center gap-2 text-xs">
          <span className="text-muted-foreground">Assignee</span>
          {internal ? (
            <AssigneeDisplay assignee={issue.assignee} />
          ) : (
            <AssigneePicker
              repo={repo}
              assignee={issue.assignee}
              onSelect={(next) => assign.mutate(next)}
              disabled={assign.isPending}
            />
          )}
        </div>
      </SheetHeader>

      <div className="flex-1 overflow-y-auto px-4 py-4">
        {editing && internal ? (
          editQuery.data ? (
            <InternalIssueForm
              repo={repo}
              issue={editQuery.data}
              onDone={() => {
                void queryClient.invalidateQueries({
                  queryKey: ["issue", repo, id],
                });
                setEditing(false);
              }}
              onCancel={() => setEditing(false)}
            />
          ) : (
            <p className="text-sm text-muted-foreground">Loading editor…</p>
          )
        ) : (
          <>
            {issue.description.trim() ? (
              <Markdown urlMap={urlMap}>{issue.description}</Markdown>
            ) : (
              <p className="text-sm text-muted-foreground">No description.</p>
            )}
            <IssueAttachments
              repo={repo}
              id={id}
              attachments={attachments}
              bodies={[
                issue.description,
                ...issue.comments.map((comment) => comment.body),
              ]}
            />
            <Comments comments={issue.comments} urlMap={urlMap} />
          </>
        )}
      </div>

      {!editing && (
        <SheetFooter className="flex-row flex-wrap items-center gap-2 border-t">
          {issue.archived ? (
            <Button
              size="sm"
              onClick={() => archive.mutate({ id, archived: false })}
              disabled={archive.isPending}
            >
              <ArchiveRestore />
              Unarchive
            </Button>
          ) : (
            <Button
              size="sm"
              onClick={() => addToQueue.mutate()}
              disabled={inQueue || addToQueue.isPending}
            >
              <ListPlus />
              {inQueue ? "Queued" : "Add to queue"}
            </Button>
          )}
          {grillable && (
            <Button variant="outline" size="sm" asChild>
              <Link to="/inbox" search={{ issue: id }}>
                <Flame />
                {activeGrill ? "Resume interview" : "Interview"}
              </Link>
            </Button>
          )}
          {internal && (
            <Button
              variant="outline"
              size="sm"
              onClick={() => setEditing(true)}
            >
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
          {!issue.archived && (
            <Button
              variant="outline"
              size="sm"
              onClick={() => archive.mutate({ id, archived: true })}
              disabled={archive.isPending}
            >
              <Archive />
              Archive
            </Button>
          )}
          <DeleteIssueDialog repo={repo} id={id} onDeleted={onClose} />
          {addToQueue.error && (
            <p className="w-full text-xs text-destructive">
              {String((addToQueue.error as Error).message)}
            </p>
          )}
          {archive.error && (
            <p className="w-full text-xs text-destructive">
              {String((archive.error as Error).message)}
            </p>
          )}
        </SheetFooter>
      )}

      {archiveNote && (
        <ArchiveToast
          message={archiveNote}
          onDismiss={() => setArchiveNote(null)}
        />
      )}
    </>
  );
}

function DrawerFrame({ id, children }: { id: string; children: ReactNode }) {
  return (
    <>
      <SheetHeader className="border-b pr-12">
        <SheetTitle className="font-mono text-sm">{id}</SheetTitle>
      </SheetHeader>
      <div className="flex-1 overflow-y-auto px-4 py-4">{children}</div>
    </>
  );
}

function Comments({
  comments,
  urlMap,
}: {
  comments: IssueComment[];
  urlMap: MarkdownUrlMap;
}) {
  if (comments.length === 0) return null;
  return (
    <div className="mt-6 flex flex-col gap-3 border-t pt-4">
      <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        Comments · {comments.length}
      </h3>
      {comments.map((comment, i) => {
        const at = comment.created_at ? when(comment.created_at) : "";
        return (
          <div
            key={i}
            className="flex flex-col gap-1 rounded-md border bg-card px-3 py-2"
          >
            <div className="flex items-center gap-2 text-xs text-muted-foreground">
              <span className="font-medium text-foreground">
                {comment.author || "unknown"}
              </span>
              {at && <span className="tabular-nums">{at}</span>}
            </div>
            <Markdown urlMap={urlMap}>{comment.body}</Markdown>
          </div>
        );
      })}
    </div>
  );
}

function FetchError({ error, id }: { error: unknown; id: string }) {
  const kind = error instanceof IssueFetchError ? error.kind : "error";

  if (kind === "not-found") {
    return (
      <Notice tone="fail" title={`${id} not found`}>
        Check the ticket id and that it exists in this repo's tracker.
      </Notice>
    );
  }

  if (kind === "no-tracker") {
    return (
      <Notice tone="warn" title="No direct tracker for this repo">
        Reading a ticket needs direct tracker credentials. Add them in{" "}
        <Link to="/settings" className="text-primary hover:underline">
          settings
        </Link>
        .
      </Notice>
    );
  }

  return (
    <p className="font-mono text-sm text-destructive">
      {error instanceof Error ? error.message : String(error)}
    </p>
  );
}

function Notice({
  tone,
  title,
  children,
}: {
  tone: "fail" | "warn";
  title: string;
  children: ReactNode;
}) {
  return (
    <div
      role="alert"
      className={cn(
        "flex items-start gap-2.5 rounded-md border px-3 py-3",
        tone === "fail"
          ? "border-fail/40 bg-fail/5"
          : "border-warn/40 bg-warn/5",
      )}
    >
      <AlertTriangle
        className={cn(
          "mt-0.5 size-3.5 shrink-0",
          tone === "fail" ? "text-fail" : "text-warn",
        )}
        aria-hidden="true"
      />
      <div className="flex flex-col gap-0.5">
        <p className="font-mono text-sm text-foreground">{title}</p>
        <p className="text-xs leading-relaxed text-muted-foreground">
          {children}
        </p>
      </div>
    </div>
  );
}

function trackerName(provider: string): string {
  return provider === "jira" ? "Jira" : "Linear";
}

function when(ts: string): string {
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleString([], { dateStyle: "medium", timeStyle: "short" });
}
