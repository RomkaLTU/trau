import { useEffect, useRef, useState, type RefObject } from "react";
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
import { SessionModeBadge } from "@/components/grill/session-mode";
import { StatusPill } from "@/components/trau";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { shouldDodge } from "@/lib/dock-dodge";
import { dockPanelAction, isDockOpenKey } from "@/lib/dock-keys";
import {
  abandonGrill,
  awaitingBreakdown,
  awaitingGrillsQueryKey,
  awaitingGrillsQueryOptions,
  awaitingWithOpen,
  dropAwaiting,
  grillDetailQueryOptions,
  isOver,
  sortAwaiting,
  type GrillAwaitingSession,
  type GrillSession,
} from "@/lib/grill";
import { formatAge } from "@/lib/ledger";
import {
  hasUnseenSession,
  useSeenMarks,
  type SeenMarks,
} from "@/lib/inbox-seen";
import { readKeyStroke } from "@/lib/keys";
import {
  grillSessionTarget,
  useNotificationEvents,
  useNotificationNavigate,
} from "@/lib/notification-center";
import { cn } from "@/lib/utils";

// Sheets and dialogs portal to the end of <body> at z-50 and switch off pointer events
// outside themselves, so matching their z-index would bury the dock — and any interview
// open in it — under whatever the user happened to open.
const dockLayer = "pointer-events-auto fixed z-[60]";

const dockAnchor = cn(dockLayer, "bottom-6 right-6");

// The dodging tab is hidden rather than unmounted, so dropping the entrance while it is
// out of the way is the only thing that leaves it to play again on the way back.
const dockEnter = "animate-in fade-in slide-in-from-bottom-2";

const UNREAD_TITLE = "A question you haven't read yet";

// A disabled editor drops the attribute, so the selector only ever finds a box that can
// actually take the keyboard.
const ANSWER_BOX = '[contenteditable="true"]';

// InterviewDock is the machine-wide answer surface: a tab pinned bottom-right
// whenever an interview anywhere is waiting on the user, expanding in place into that
// conversation so the question is answered without leaving the screen — or the
// project — the user is on. It rides every route; on the Inbox, which owns the full
// triage surface, the tab points that page at the session instead of floating a
// second copy of the conversation over it.
export function InterviewDock() {
  const onInbox = useRouterState({
    select: (s) => s.location.pathname.startsWith("/inbox"),
  });
  const queryClient = useQueryClient();
  const frame = useRef<HTMLDivElement>(null);
  const [engaged, setEngaged] = useState<GrillAwaitingSession | null>(null);
  const [expanded, setExpanded] = useState(false);
  const [seen, read] = useSeenMarks();
  const { data } = useQuery(awaitingGrillsQueryOptions());
  const focusInPage = useNotificationNavigate();

  useNotificationEvents((notification) => {
    if (notification.kind !== "grill_question") return;
    void queryClient.invalidateQueries({ queryKey: awaitingGrillsQueryKey });
  });

  const awaiting = sortAwaiting(data?.sessions ?? []);
  const engagedRow = engaged
    ? awaiting.find((s) => s.id === engaged.id)
    : undefined;

  // Answering drops the engaged session off the awaiting feed while the interviewer
  // works. The dock keeps holding it either way, so minimising leaves a tab rather
  // than taking the conversation away, and the next question lands back in it.
  const inflight = engaged && !engagedRow ? engaged : null;
  const showPanel = engaged !== null && expanded && !onInbox;

  // Minimised, the dock has no stream on the session it holds, so its own resource is
  // the only signal separating an interviewer still working from a session that is over.
  const { data: held, isError: heldLost } = useQuery({
    ...grillDetailQueryOptions(inflight && !showPanel ? inflight.id : ""),
    refetchInterval: 5_000,
  });
  const heldState = held?.session.state;
  // A read that fails for good leaves nothing to hold — a session purged with its issue
  // is as gone as one that ended, and holding it would leave the tab thinking forever.
  useEffect(() => {
    if (heldLost || (heldState && isOver(heldState))) setEngaged(null);
  }, [heldLost, heldState]);

  // The Inbox owns the conversation, so leaving it never resurrects the panel.
  useEffect(() => {
    if (onInbox) setExpanded(false);
  }, [onInbox]);

  const open = (session: GrillAwaitingSession) => {
    setEngaged(session);
    setExpanded(true);
    read(session);
  };

  const letGo = () => {
    setEngaged(null);
    setExpanded(false);
  };

  const activate = (session: GrillAwaitingSession) => {
    const target = grillSessionTarget(session);
    // A research question is answered next to its report, which the panel has no
    // room for, so the dock hands the session over instead of holding it.
    if (target.kind === "research") {
      read(session);
      letGo();
      focusInPage(target);
      return;
    }
    if (!onInbox) {
      open(session);
      return;
    }
    focusInPage(target);
  };

  // The tab leads with the session the dock holds, so what the user was last in stays
  // one click away instead of the top row taking its place. Off the feed the held row
  // is a snapshot from before the answer, whose question is already spent.
  const tab = inflight
    ? {
        ...inflight,
        state: "running" as const,
        updated_at: held?.session.updated_at ?? inflight.updated_at,
        question: undefined,
      }
    : (engagedRow ?? awaiting[0]);

  // i opens whatever the tab opens — the Inbox's own thread there, the panel anywhere
  // else — and hands an open panel the keyboard back when the user has clicked away.
  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if (!isDockOpenKey(readKeyStroke(e))) return;
      if (showPanel) {
        frame.current?.focus();
        return;
      }
      if (tab) activate(tab);
    }
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [activate, showPanel, tab]);

  if (showPanel && engaged) {
    return (
      <InterviewPanel
        frame={frame}
        session={engaged}
        sessions={awaitingWithOpen(awaiting, engaged)}
        seen={seen}
        onSelect={open}
        onRead={read}
        onCollapse={() => setExpanded(false)}
        onDismiss={letGo}
      />
    );
  }

  if (!tab) return null;
  const standsFor = awaitingWithOpen(awaiting, tab);

  return (
    <InterviewTab
      session={tab}
      feed={{
        thinking: inflight !== null,
        count: standsFor.length,
        breakdown: awaitingBreakdown(standsFor),
        unread: awaiting.some((s) => hasUnseenSession(seen, s)),
      }}
      onActivate={() => activate(tab)}
      onAbandoned={letGo}
    />
  );
}

// InterviewFeed is everything the dock says about the sessions its tab stands for, so
// the full tab and the pill it collapses to are always speaking about the same thing.
interface InterviewFeed {
  thinking: boolean;
  count: number;
  breakdown: string;
  unread: boolean;
}

function interviewLabel({ thinking }: InterviewFeed): string {
  return thinking ? "Interviewer is thinking…" : "Interview waiting";
}

function InterviewStatus({ thinking, unread }: InterviewFeed) {
  if (thinking) {
    return (
      <Loader2
        className="size-3 shrink-0 animate-spin text-teal"
        aria-hidden="true"
      />
    );
  }

  return (
    <span
      className={cn(
        "size-2 shrink-0 rounded-full",
        unread ? "animate-pulse bg-warn" : "bg-info/60",
      )}
      aria-hidden="true"
      title={unread ? UNREAD_TITLE : undefined}
    />
  );
}

function InterviewTab({
  session,
  feed,
  onActivate,
  onAbandoned,
}: {
  session: GrillAwaitingSession;
  feed: InterviewFeed;
  onActivate: () => void;
  onAbandoned: () => void;
}) {
  const card = useRef<HTMLDivElement>(null);
  const dodging = useDodging(card);

  return (
    <>
      <div
        ref={card}
        className={cn(
          dockAnchor,
          "flex w-80 max-w-[calc(100vw-3rem)] flex-col rounded-lg border bg-card shadow-lg transition-colors hover:bg-accent",
          dodging ? "invisible" : dockEnter,
        )}
      >
        <button
          type="button"
          onClick={onActivate}
          className="flex flex-col gap-1 p-3 text-left"
        >
          <span className="flex items-center gap-2 pr-7">
            <InterviewStatus {...feed} />
            <span className="flex-1 truncate text-xs font-medium text-muted-foreground">
              {interviewLabel(feed)}
            </span>
            {feed.count > 1 && (
              <Badge
                variant="secondary"
                title={feed.breakdown}
                className="shrink-0 font-mono text-[10px] tabular-nums"
              >
                ×{feed.count}
              </Badge>
            )}
          </span>
          {feed.count > 1 && (
            <span className="truncate text-[11px] text-muted-foreground">
              {feed.breakdown}
            </span>
          )}
          <SessionFacts session={session} />
        </button>
        {/* Keyed so a re-sorted feed drops an open confirm instead of re-aiming it. */}
        <AbandonAction
          key={session.id}
          session={session}
          onAbandoned={onAbandoned}
        />
      </div>
      {dodging && <DodgedTab feed={feed} onActivate={onActivate} />}
    </>
  );
}

// useDodging watches the field the keyboard is in and reports when the tab is over it.
// The tab is hidden rather than unmounted so its own box stays the one shouldDodge
// judges against.
function useDodging(tab: RefObject<HTMLElement | null>): boolean {
  const [dodging, setDodging] = useState(false);

  useEffect(() => {
    const card = tab.current;
    let field: Element | null = null;

    const judge = () =>
      setDodging(shouldDodge(field, card?.getBoundingClientRect() ?? null));

    // The composer grows upward as an answer wraps and the tab grows as the feed fills,
    // so either can walk into the other long after the field took the keyboard.
    const sizes = new ResizeObserver(judge);
    if (card) sizes.observe(card);

    const follow = (next: Element | null) => {
      if (field) sizes.unobserve(field);
      field = next;
      if (field) sizes.observe(field);
      judge();
    };

    const onFocusIn = (e: FocusEvent) => follow(e.target as Element | null);
    // Where the focus is going, not what it left: a move straight from one field to the
    // next would otherwise flash the tab back over both of them.
    const onFocusOut = (e: FocusEvent) =>
      follow(e.relatedTarget as Element | null);

    follow(document.activeElement);
    document.addEventListener("focusin", onFocusIn);
    document.addEventListener("focusout", onFocusOut);
    window.addEventListener("resize", judge);
    return () => {
      sizes.disconnect();
      document.removeEventListener("focusin", onFocusIn);
      document.removeEventListener("focusout", onFocusOut);
      window.removeEventListener("resize", judge);
    };
  }, [tab]);

  return dodging;
}

// DodgedTab is the tab out of the typing's way: a pill small enough to clear the
// composer's own controls, opening the same interview.
function DodgedTab({
  feed,
  onActivate,
}: {
  feed: InterviewFeed;
  onActivate: () => void;
}) {
  const label = interviewLabel(feed);

  return (
    <button
      type="button"
      onClick={onActivate}
      // Taking the keyboard off the field would put the tab back under the cursor
      // between the press and the release, and the click would land on nothing.
      onMouseDown={(e) => e.preventDefault()}
      title={feed.count > 1 ? `${label} — ${feed.breakdown}` : label}
      className={cn(
        dockLayer,
        "animate-in fade-in zoom-in-95 bottom-2 right-2 flex h-9 min-w-9 items-center justify-center gap-1.5 rounded-full border bg-card px-2 shadow-lg transition-colors hover:bg-accent",
      )}
    >
      <InterviewStatus {...feed} />
      {feed.count > 1 && (
        <span className="font-mono text-[10px] tabular-nums text-muted-foreground">
          ×{feed.count}
        </span>
      )}
      <span className="sr-only">{label}</span>
    </button>
  );
}

// InterviewPanel frames the conversation itself. The session it mounts is the dock's
// own — never the active scope's — so answering another project's question leaves the
// project switcher where the user left it. The frame takes the keyboard as it opens, so
// the panel is walked, answered and closed without the mouse ever coming into it.
function InterviewPanel({
  frame,
  session,
  sessions,
  seen,
  onSelect,
  onRead,
  onCollapse,
  onDismiss,
}: {
  frame: RefObject<HTMLDivElement | null>;
  session: GrillAwaitingSession;
  sessions: GrillAwaitingSession[];
  seen: SeenMarks;
  onSelect: (session: GrillAwaitingSession) => void;
  onRead: (session: GrillSession) => void;
  onCollapse: () => void;
  onDismiss: () => void;
}) {
  const navigateToNotification = useNotificationNavigate();
  const [status, setStatus] = useState<GrillSession | null>(null);
  const [confirming, setConfirming] = useState(false);
  // A switch leaves the outgoing session's status behind for a render, so the frame
  // only quotes a status that belongs to the conversation it is mounting.
  const live = status?.id === session.id ? status : session;
  const pill = statePill(live.state);

  useEffect(() => {
    onRead(live);
  }, [onRead, live.id, live.updated_at]);

  useEffect(() => {
    frame.current?.focus();
  }, [frame]);

  // A confirm is aimed at the session it was raised on, so a switch drops it.
  useEffect(() => setConfirming(false), [session.id]);

  const review = () => {
    onDismiss();
    navigateToNotification(grillSessionTarget(session));
  };

  const step = (by: number) => {
    const to = sessions[sessions.findIndex((s) => s.id === session.id) + by];
    if (to) onSelect(to);
  };

  // The bindings sit on the document rather than the frame: an answer sent closes the
  // box it was typed in, dropping focus to the page, and keys hung off the panel would
  // go with it.
  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      const action = dockPanelAction(readKeyStroke(e));
      switch (action) {
        case "next":
        case "prev":
          // Left to the browser the arrows scroll the thread out from under the switch.
          e.preventDefault();
          step(action === "next" ? 1 : -1);
          break;
        case "answer":
          frame.current?.querySelector<HTMLElement>(ANSWER_BOX)?.focus();
          break;
        case "abandon":
          setConfirming(true);
          break;
        case "collapse":
          onCollapse();
          break;
      }
    }
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [frame, session, sessions, onSelect, onCollapse]);

  return (
    <div
      ref={frame}
      role="dialog"
      aria-label={`Interview — ${sessionTitle(session)}`}
      tabIndex={-1}
      className={cn(
        dockAnchor,
        dockEnter,
        "flex max-h-[68vh] w-[520px] max-w-[calc(100vw-3rem)] flex-col overflow-hidden rounded-lg border bg-card shadow-xl outline-none",
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
              if (s.id === session.id) onDismiss();
            }}
          />
        )}
        <SessionModeBadge mode={session.mode} />
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
          activity={false}
          onStatus={(s) => setStatus(s.session)}
          onReview={review}
        />
      </div>

      {confirming && (
        <AbandonConfirm
          session={session}
          autoFocus
          onKeep={() => {
            setConfirming(false);
            frame.current?.focus();
          }}
          onAbandoned={onDismiss}
        />
      )}
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
        <SessionModeBadge mode={session.mode} />
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

function AbandonAction({
  session,
  onAbandoned,
}: {
  session: GrillAwaitingSession;
  onAbandoned?: (session: GrillAwaitingSession) => void;
}) {
  const [confirming, setConfirming] = useState(false);

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
    <AbandonConfirm
      session={session}
      onKeep={() => setConfirming(false)}
      onAbandoned={onAbandoned}
    />
  );
}

// The confirm stays inside the row: a dialog portals underneath the dock, and one
// raised from the switcher would dismiss the popover it lives in.
function AbandonConfirm({
  session,
  autoFocus,
  onKeep,
  onAbandoned,
}: {
  session: GrillAwaitingSession;
  autoFocus?: boolean;
  onKeep: () => void;
  onAbandoned?: (session: GrillAwaitingSession) => void;
}) {
  const queryClient = useQueryClient();
  const confirm = useRef<HTMLDivElement>(null);
  const abandon = useMutation({
    mutationFn: () => abandonGrill(session.id),
    onSuccess: () => {
      dropAwaiting(queryClient, session.id);
      void queryClient.invalidateQueries({ queryKey: awaitingGrillsQueryKey });
      onAbandoned?.(session);
    },
  });

  // The switcher's list scrolls, so a confirm raised on its last row opens off-screen.
  useEffect(() => {
    confirm.current?.scrollIntoView({ block: "nearest" });
  }, []);

  return (
    <div
      ref={confirm}
      // The keys asked here are the confirm's own; Escape would otherwise carry on and
      // collapse the panel behind it.
      onKeyDown={(e) => {
        e.stopPropagation();
        if (e.key === "Escape") onKeep();
      }}
      className="flex shrink-0 flex-col gap-1 border-t px-3 py-2"
    >
      <div className="flex items-center gap-2">
        <span className="flex-1 text-[11px] text-muted-foreground">
          Abandon this interview?
        </span>
        <Button
          variant="ghost"
          size="sm"
          className="h-7 px-2 text-xs"
          onClick={onKeep}
          disabled={abandon.isPending}
        >
          Keep
        </Button>
        <Button
          variant="destructive"
          size="sm"
          className="h-7 px-2 text-xs"
          autoFocus={autoFocus}
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
