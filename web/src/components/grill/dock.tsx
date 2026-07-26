import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRouterState } from "@tanstack/react-router";
import {
  Check,
  ChevronDown,
  ChevronsUpDown,
  Clock,
  Loader2,
  Trash2,
} from "lucide-react";

import { statePill } from "@/components/grill/banners";
import { GrillConversation } from "@/components/grill/conversation";
import { StatusPill } from "@/components/trau";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import {
  abandonGrill,
  awaitingBreakdown,
  awaitingGrillsQueryKey,
  awaitingGrillsQueryOptions,
  awaitingWithOpen,
  awaitingWithout,
  sortAwaiting,
  type GrillAwaitingResponse,
  type GrillAwaitingSession,
  type GrillSession,
} from "@/lib/grill";
import { formatAge } from "@/lib/ledger";
import { draftItemId } from "@/lib/inbox";
import {
  hasUnseenSession,
  useSeenMarks,
  type SeenMarks,
} from "@/lib/inbox-seen";
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

const UNREAD_TITLE = "A question you haven't read yet";

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
  const [seen, read] = useSeenMarks();
  const { data } = useQuery({
    ...awaitingGrillsQueryOptions(),
    enabled: !onInbox,
  });

  useNotificationEvents((notification) => {
    if (notification.kind !== "grill_question") return;
    void queryClient.invalidateQueries({ queryKey: awaitingGrillsQueryKey });
  });

  const expand = (session: GrillAwaitingSession) => {
    setExpanded(session);
    read(session);
  };

  if (onInbox) return null;

  const awaiting = sortAwaiting(data?.sessions ?? []);

  // Answering drops the session off the awaiting feed while the agent thinks, so an
  // expanded panel outlives the row that opened it and the next question streams in
  // place rather than reopening as a fresh tab.
  if (expanded) {
    return (
      <InterviewPanel
        session={expanded}
        sessions={awaitingWithOpen(awaiting, expanded)}
        seen={seen}
        onSelect={expand}
        onRead={read}
        onCollapse={() => setExpanded(null)}
      />
    );
  }

  const top = awaiting[0];
  if (!top) return null;

  return (
    <InterviewTab
      session={top}
      count={awaiting.length}
      breakdown={awaitingBreakdown(awaiting)}
      unread={awaiting.some((s) => hasUnseenSession(seen, s))}
      onExpand={() => expand(top)}
    />
  );
}

function InterviewTab({
  session,
  count,
  breakdown,
  unread,
  onExpand,
}: {
  session: GrillAwaitingSession;
  count: number;
  breakdown: string;
  unread: boolean;
  onExpand: () => void;
}) {
  return (
    <div
      className={cn(
        dockAnchor,
        "flex w-80 max-w-[calc(100vw-3rem)] flex-col rounded-lg border bg-card shadow-lg transition-colors hover:bg-accent",
      )}
    >
      <button
        type="button"
        onClick={onExpand}
        className="flex flex-col gap-1 p-3 text-left"
      >
        <span className="flex items-center gap-2 pr-7">
          <span
            className={cn(
              "size-2 shrink-0 rounded-full",
              unread ? "animate-pulse bg-warn" : "bg-info/60",
            )}
            aria-hidden="true"
            title={unread ? UNREAD_TITLE : undefined}
          />
          <span className="flex-1 truncate text-xs font-medium text-muted-foreground">
            Interview waiting
          </span>
          {count > 1 && (
            <Badge
              variant="secondary"
              title={breakdown}
              className="shrink-0 font-mono text-[10px] tabular-nums"
            >
              ×{count}
            </Badge>
          )}
        </span>
        {count > 1 && (
          <span className="truncate text-[11px] text-muted-foreground">
            {breakdown}
          </span>
        )}
        <SessionFacts session={session} />
      </button>
      {/* Keyed so a re-sorted feed drops an open confirm instead of re-aiming it. */}
      <AbandonAction key={session.id} session={session} />
    </div>
  );
}

// InterviewPanel frames the conversation itself. The session it mounts is the dock's
// own — never the active scope's — so answering another project's question leaves the
// project switcher where the user left it.
function InterviewPanel({
  session,
  sessions,
  seen,
  onSelect,
  onRead,
  onCollapse,
}: {
  session: GrillAwaitingSession;
  sessions: GrillAwaitingSession[];
  seen: SeenMarks;
  onSelect: (session: GrillAwaitingSession) => void;
  onRead: (session: GrillSession) => void;
  onCollapse: () => void;
}) {
  const navigateToNotification = useNotificationNavigate();
  const [status, setStatus] = useState<GrillSession | null>(null);
  // A switch leaves the outgoing session's status behind for a render, so the frame
  // only quotes a status that belongs to the conversation it is mounting.
  const live = status?.id === session.id ? status : session;
  const pill = statePill(live.state);

  useEffect(() => {
    onRead(live);
  }, [onRead, live.id, live.updated_at]);

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
        {sessions.length > 1 && (
          <InterviewSwitcher
            sessions={sessions}
            current={session}
            seen={seen}
            onSelect={onSelect}
            onAbandoned={(s) => {
              if (s.id === session.id) onCollapse();
            }}
          />
        )}
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
          onStatus={(s) => setStatus(s.session)}
          onReview={review}
        />
      </div>
    </div>
  );
}

// InterviewSwitcher moves the panel between the sessions waiting on the user without
// collapsing it. Picking one is what mounts it — the list re-sorts under the panel on
// every poll, and none of that moves the conversation the user is in.
function InterviewSwitcher({
  sessions,
  current,
  seen,
  onSelect,
  onAbandoned,
}: {
  sessions: GrillAwaitingSession[];
  current: GrillAwaitingSession;
  seen: SeenMarks;
  onSelect: (session: GrillAwaitingSession) => void;
  onAbandoned: (session: GrillAwaitingSession) => void;
}) {
  const [open, setOpen] = useState(false);
  const at = sessions.findIndex((s) => s.id === current.id);

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        aria-label="Switch interview"
        className="flex shrink-0 items-center gap-1 rounded-md px-1.5 py-1 font-mono text-[11px] text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
      >
        {at + 1} of {sessions.length}
        <ChevronsUpDown className="size-3" aria-hidden="true" />
      </PopoverTrigger>
      <PopoverContent
        align="end"
        sideOffset={8}
        // The popover portals out of the panel but stays inside its React tree, so its
        // own dismissal would bubble on and collapse the panel behind it.
        onKeyDown={(e) => e.stopPropagation()}
        className="z-[70] w-80 p-1"
      >
        <ul className="flex max-h-64 flex-col gap-0.5 overflow-y-auto">
          {sessions.map((s) => (
            <SwitcherRow
              key={s.id}
              session={s}
              current={s.id === current.id}
              unread={hasUnseenSession(seen, s)}
              onSelect={() => {
                setOpen(false);
                if (s.id !== current.id) onSelect(s);
              }}
              onAbandoned={onAbandoned}
            />
          ))}
        </ul>
      </PopoverContent>
    </Popover>
  );
}

function SwitcherRow({
  session,
  current,
  unread,
  onSelect,
  onAbandoned,
}: {
  session: GrillAwaitingSession;
  current: boolean;
  unread: boolean;
  onSelect: () => void;
  onAbandoned: (session: GrillAwaitingSession) => void;
}) {
  return (
    <li className="relative">
      <button
        type="button"
        onClick={onSelect}
        className={cn(
          "flex w-full items-start gap-2 rounded-md px-2 py-1.5 text-left transition-colors hover:bg-accent",
          current && "bg-secondary",
        )}
      >
        <Check
          className={cn(
            "mt-0.5 size-3 shrink-0 text-primary",
            !current && "opacity-0",
          )}
          aria-hidden="true"
        />
        <span className="flex min-w-0 flex-1 flex-col gap-0.5 pr-6">
          <SessionFacts session={session} unread={unread} />
        </span>
      </button>
      <AbandonAction session={session} onAbandoned={onAbandoned} />
    </li>
  );
}

function SessionFacts({
  session,
  unread,
}: {
  session: GrillAwaitingSession;
  unread?: boolean;
}) {
  const pill = statePill(session.state);

  return (
    <>
      <span className="flex min-w-0 items-center gap-1.5">
        {session.issue_id ? (
          <span className="shrink-0 font-mono text-[11px] text-muted-foreground">
            {session.issue_id}
          </span>
        ) : (
          <span className="shrink-0 rounded border border-dashed px-1 font-mono text-[10px] text-muted-foreground">
            draft
          </span>
        )}
        <span className="min-w-0 flex-1 truncate text-sm font-medium text-foreground">
          {sessionTitle(session)}
        </span>
        {unread && (
          <span
            className="size-1.5 shrink-0 rounded-full bg-warn"
            aria-hidden="true"
            title={UNREAD_TITLE}
          />
        )}
      </span>
      {session.question && (
        <span className="truncate text-xs text-muted-foreground">
          {session.question}
        </span>
      )}
      <span className="flex items-center gap-2">
        <StatusPill state={pill.state} label={pill.label} />
        <span className="min-w-0 flex-1 truncate font-mono text-[10px] text-muted-foreground">
          {session.repo}
        </span>
        <SessionAge at={session.updated_at} />
      </span>
    </>
  );
}

function SessionAge({ at }: { at: string }) {
  const ts = Date.parse(at);
  if (Number.isNaN(ts)) return null;

  return (
    <span
      className="flex shrink-0 items-center gap-1 font-mono text-[10px] text-muted-foreground"
      title={`Last activity ${new Date(ts).toLocaleString()}`}
    >
      <Clock className="size-3" aria-hidden="true" />
      {formatAge(Date.now() - ts)}
    </span>
  );
}

// The confirm stays inside the row: a dialog portals underneath the dock, and one
// raised from the switcher would dismiss the popover it lives in.
function AbandonAction({
  session,
  onAbandoned,
}: {
  session: GrillAwaitingSession;
  onAbandoned?: (session: GrillAwaitingSession) => void;
}) {
  const queryClient = useQueryClient();
  const [confirming, setConfirming] = useState(false);
  const confirm = useRef<HTMLDivElement>(null);
  const abandon = useMutation({
    mutationFn: () => abandonGrill(session.id),
    onSuccess: () => {
      queryClient.setQueryData<GrillAwaitingResponse>(
        awaitingGrillsQueryKey,
        (feed) =>
          feed && { sessions: awaitingWithout(feed.sessions, session.id) },
      );
      void queryClient.invalidateQueries({ queryKey: awaitingGrillsQueryKey });
      onAbandoned?.(session);
    },
  });

  // The switcher's list scrolls, so a confirm raised on its last row opens off-screen.
  useEffect(() => {
    confirm.current?.scrollIntoView({ block: "nearest" });
  }, [confirming]);

  if (!confirming) {
    return (
      <Button
        variant="ghost"
        size="icon"
        className="absolute right-1.5 top-1.5 size-7 text-muted-foreground hover:bg-fail/10 hover:text-fail"
        onClick={() => setConfirming(true)}
        title="Abandon interview"
      >
        <Trash2 className="size-3.5" />
        <span className="sr-only">Abandon interview</span>
      </Button>
    );
  }

  return (
    <div ref={confirm} className="flex flex-col gap-1 border-t px-3 py-2">
      <div className="flex items-center gap-2">
        <span className="flex-1 text-[11px] text-muted-foreground">
          Abandon this interview?
        </span>
        <Button
          variant="ghost"
          size="sm"
          className="h-7 px-2 text-xs"
          onClick={() => setConfirming(false)}
          disabled={abandon.isPending}
        >
          Keep
        </Button>
        <Button
          variant="destructive"
          size="sm"
          className="h-7 px-2 text-xs"
          onClick={() => abandon.mutate()}
          disabled={abandon.isPending}
        >
          {abandon.isPending && <Loader2 className="animate-spin" />}
          Abandon
        </Button>
      </div>
      {abandon.error && (
        <span className="text-[11px] text-fail">{abandon.error.message}</span>
      )}
    </div>
  );
}

// sessionTitle names a session: the issue it grills, or an authoring draft's seed —
// which a draft opened and not yet described does not have.
function sessionTitle(session: GrillAwaitingSession): string {
  return session.issue_title || session.issue_id || "New issue draft";
}
