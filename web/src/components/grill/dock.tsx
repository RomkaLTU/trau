import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRouterState } from "@tanstack/react-router";
import { ChevronDown } from "lucide-react";

import { statePill } from "@/components/grill/banners";
import { GrillConversation } from "@/components/grill/conversation";
import { StatusPill } from "@/components/trau";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  awaitingGrillsQueryKey,
  awaitingGrillsQueryOptions,
  sortAwaiting,
  type GrillAwaitingSession,
  type GrillSession,
} from "@/lib/grill";
import { draftItemId } from "@/lib/inbox";
import {
  useNotificationEvents,
  useNotificationNavigate,
} from "@/lib/notification-center";
import { cn } from "@/lib/utils";

// Sheets and dialogs portal to the end of <body> at z-50 and switch off pointer events
// outside themselves, so matching their z-index would bury the dock — and any interview
// open in it — under whatever the user happened to open.
const dockAnchor =
  "animate-in fade-in slide-in-from-bottom-2 pointer-events-auto fixed bottom-6 right-6 z-[60]";

// InterviewDock is the machine-wide answer surface: a tab pinned bottom-right
// whenever an interview anywhere is waiting on the user, expanding in place into that
// conversation so the question is answered without leaving the screen — or the
// project — the user is on. The Inbox owns the full triage surface, so the dock stays
// off it.
export function InterviewDock() {
  const onInbox = useRouterState({
    select: (s) => s.location.pathname.startsWith("/inbox"),
  });
  const queryClient = useQueryClient();
  const [expanded, setExpanded] = useState<GrillAwaitingSession | null>(null);
  const { data } = useQuery({
    ...awaitingGrillsQueryOptions(),
    enabled: !onInbox,
  });

  useNotificationEvents((notification) => {
    if (notification.kind !== "grill_question") return;
    void queryClient.invalidateQueries({ queryKey: awaitingGrillsQueryKey });
  });

  if (onInbox) return null;

  // Answering drops the session off the awaiting feed while the agent thinks, so an
  // expanded panel outlives the row that opened it and the next question streams in
  // place rather than reopening as a fresh tab.
  if (expanded) {
    return (
      <InterviewPanel
        session={expanded}
        onCollapse={() => setExpanded(null)}
      />
    );
  }

  const top = sortAwaiting(data?.sessions ?? [])[0];
  if (!top) return null;

  return <InterviewTab session={top} onExpand={() => setExpanded(top)} />;
}

function InterviewTab({
  session,
  onExpand,
}: {
  session: GrillAwaitingSession;
  onExpand: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onExpand}
      className={cn(
        dockAnchor,
        "flex w-80 max-w-[calc(100vw-3rem)] flex-col gap-1 rounded-lg border bg-card p-3 text-left shadow-lg transition-colors hover:bg-accent",
      )}
    >
      <div className="flex items-center gap-2">
        <span
          className="size-2 shrink-0 animate-pulse rounded-full bg-info"
          aria-hidden="true"
        />
        <span className="flex-1 text-xs font-medium text-muted-foreground">
          Interview waiting
        </span>
        <Badge variant="outline" className="shrink-0 font-mono text-[10px]">
          {session.repo}
        </Badge>
      </div>
      <span className="truncate text-sm font-medium text-foreground">
        {sessionTitle(session)}
      </span>
      {session.question && (
        <span className="truncate text-xs text-muted-foreground">
          {session.question}
        </span>
      )}
    </button>
  );
}

// InterviewPanel frames the conversation itself. The session it mounts is the dock's
// own — never the active scope's — so answering another project's question leaves the
// project switcher where the user left it.
function InterviewPanel({
  session,
  onCollapse,
}: {
  session: GrillAwaitingSession;
  onCollapse: () => void;
}) {
  const navigateToNotification = useNotificationNavigate();
  const [live, setLive] = useState<GrillSession>(session);
  const pill = statePill(live.state);

  const review = () => {
    onCollapse();
    navigateToNotification({
      kind: "inbox",
      repo: session.repo,
      issue: session.issue_id || draftItemId(session.id),
    });
  };

  return (
    <div
      role="dialog"
      aria-label={`Interview — ${sessionTitle(session)}`}
      onKeyDown={(e) => {
        if (e.key === "Escape") onCollapse();
      }}
      className={cn(
        dockAnchor,
        "flex max-h-[68vh] w-[520px] max-w-[calc(100vw-3rem)] flex-col overflow-hidden rounded-lg border bg-card shadow-xl",
      )}
    >
      <div className="flex shrink-0 items-center gap-2 border-b px-4 py-2.5">
        <div className="flex min-w-0 flex-1 flex-col">
          <span className="truncate text-sm font-medium text-foreground">
            {sessionTitle(session)}
          </span>
          <span className="truncate font-mono text-[11px] text-muted-foreground">
            {session.repo}
          </span>
        </div>
        <StatusPill state={pill.state} label={pill.label} />
        <Button
          variant="ghost"
          size="icon"
          className="size-8"
          onClick={onCollapse}
          title="Collapse interview"
        >
          <ChevronDown />
          <span className="sr-only">Collapse interview</span>
        </Button>
      </div>

      <div className="flex min-h-0 flex-1 flex-col">
        <GrillConversation
          key={session.id}
          repo={session.repo}
          initial={session}
          outcome="link"
          onStatus={(status) => setLive(status.session)}
          onReview={review}
        />
      </div>
    </div>
  );
}

// sessionTitle names a session: the issue it grills, or an authoring draft's seed —
// which a draft opened and not yet described does not have.
function sessionTitle(session: GrillAwaitingSession): string {
  return session.issue_title || session.issue_id || "New issue draft";
}
