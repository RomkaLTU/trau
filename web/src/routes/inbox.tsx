import { useEffect, useState } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { parseAsString, useQueryState } from "nuqs";
import {
  Clock,
  Loader2,
  PanelRightClose,
  PanelRightOpen,
  RotateCcw,
  SkipForward,
  Sparkles,
  Square,
  Trash2,
} from "lucide-react";

import { Markdown } from "@/components/markdown";
import { CreatedToast } from "@/components/created-toast";
import { ErrorNote } from "@/components/grill/banners";
import { Composer } from "@/components/grill/composer";
import {
  GrillConversation,
  type GrillStatus,
} from "@/components/grill/conversation";
import { useGrillSession } from "@/components/grill/session";
import { Button } from "@/components/ui/button";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
} from "@/components/ui/select";
import {
  AssigneeAvatar,
  PageHeader,
  ProjectScopeGate,
  RepoHealthGate,
  StatusPill,
  useActiveRepo,
} from "@/components/trau";
import {
  abandonGrill,
  GRILLABLE_LABELS,
  isSettled,
  latestOutcome,
  outcomePayload,
  pregrillIssues,
  startGrillSession,
  switchGrillModel,
  type GrillAppliedOutcome,
  type GrillListResponse,
  type GrillSession,
  type OutcomePayload,
} from "@/lib/grill";
import {
  contextRows,
  loadContextOpen,
  storeContextOpen,
} from "@/lib/inbox-context";
import { issueQueryOptions } from "@/lib/issues";
import {
  draftItemId,
  inboxGroups,
  inboxPill,
  inboxPosition,
  nextIssueId,
  newDraftItem,
  NEW_DRAFT_ID,
  prevIssueId,
  rowSession,
  selectedItem,
  skipTarget,
  summarisePregrill,
  useInboxQueue,
  type InboxGroup,
  type InboxGroupView,
  type InboxItem,
  type InboxPillTone,
} from "@/lib/inbox";
import { hasOpenLayer, inboxKeyAction } from "@/lib/inbox-keys";
import {
  hasUnseenQuestion,
  loadSeen,
  markSeen,
  storeSeen,
  type SeenMarks,
} from "@/lib/inbox-seen";
import { standardTitle, usePageTitle } from "@/lib/page-title";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/inbox")({
  component: InboxPage,
  // issue selects a queue row (or draft:new opens a fresh Draft) — read at runtime
  // through nuqs, typed here so backlog and the issue drawer can link into it.
  validateSearch: (search: Record<string, unknown>): { issue?: string } =>
    typeof search.issue === "string" && search.issue !== ""
      ? { issue: search.issue }
      : {},
});

const GROUP_COUNT_TONE: Record<InboxGroup, string> = {
  waiting: "text-warn",
  review: "text-teal",
  done: "text-done",
};

const PILL_TEXT_TONE: Record<InboxPillTone, string> = {
  warn: "text-warn",
  active: "text-teal",
  verify: "text-info",
  success: "text-done",
  todo: "text-faint",
};

function InboxPage() {
  usePageTitle(standardTitle("Inbox"));
  const { repo: activeRepo } = useActiveRepo();
  const repo = activeRepo ?? "";
  const queryClient = useQueryClient();
  const {
    items: queueItems,
    groups: queueGroups,
    done,
    isLoading,
    error,
  } = useInboxQueue(repo);

  const [peek, setPeek] = useQueryState(
    "issue",
    parseAsString.withOptions({ history: "push" }),
  );

  // The fresh Draft is client-only until its first message starts a session. It lives
  // as a synthetic row at the head of the queue, seeded when New issue selects it (or
  // a backlog link arrives at ?issue=draft:new), and evaporates the moment selection
  // moves off it untouched.
  const [newDraft, setNewDraft] = useState(() => peek === NEW_DRAFT_ID);
  const items = newDraft ? [newDraftItem(), ...queueItems] : queueItems;
  const groups = newDraft ? inboxGroups(items, done) : queueGroups;

  // The queue owns the selection: Done today rows are openable for reference, and an
  // ?issue= naming something in neither list falls back to the head rather than
  // opening a session on a stray id.
  const selected = selectedItem(items, done, peek);

  const [contextOpen, setContextOpen] = useState(loadContextOpen);
  const [passSummary, setPassSummary] = useState<string | null>(null);
  const [status, setStatus] = useState<GrillStatus | null>(null);
  const [seen, setSeen] = useState<SeenMarks>(loadSeen);
  const [created, setCreated] = useState<GrillAppliedOutcome | null>(null);
  const navigate = useNavigate();

  useEffect(() => {
    if (!created) return;
    const id = setTimeout(() => setCreated(null), 8000);
    return () => clearTimeout(id);
  }, [created]);

  useEffect(() => {
    if (newDraft && peek !== NEW_DRAFT_ID) setNewDraft(false);
  }, [newDraft, peek]);

  // The thread reports the session it is following, but the panel beside it must not
  // read the outgoing item's status while a freshly selected one is still mounting.
  // An issue matches on its id; an issue-less draft has none, so it matches the
  // session it is showing.
  const followsSelected =
    status !== null &&
    selected !== null &&
    (selected.draft
      ? selected.session?.id === status.session.id
      : status.session.issue_id === selected.id);
  const live = followsSelected ? status : null;

  // Whatever is on screen has been read, and the thread's session is the one being
  // read — it is followed live, where the rail's list trails a staleTime behind.
  const onScreen = live?.session ?? selected?.session;

  // With no session the chat zone shows the read-only preview, which carries the issue
  // itself — so the context panel beside it would only repeat what is already in view.
  // A draft has no tracker issue behind it, so it never opens the context panel.
  const hasSession = Boolean(onScreen);
  const showContext = hasSession && !selected?.draft;

  useEffect(() => {
    if (!onScreen) return;
    setSeen((marks) => markSeen(marks, onScreen.id, onScreen.updated_at));
  }, [onScreen?.id, onScreen?.updated_at]);

  useEffect(() => {
    storeSeen(seen);
  }, [seen]);

  function toggleContext() {
    const next = !contextOpen;
    setContextOpen(next);
    storeContextOpen(next);
  }

  const untouchedIds = items
    .filter((item) => item.attention === "open")
    .map((item) => item.id);

  const pregrillAll = useMutation({
    mutationFn: () => pregrillIssues(repo, untouchedIds),
    onSuccess: (res) => setPassSummary(summarisePregrill(res)),
    onSettled: () =>
      void queryClient.invalidateQueries({ queryKey: ["grill", repo] }),
  });

  // Skipping parks nothing: an untouched session settles server-side on idle, so the
  // item keeps its place in the queue and comes round again.
  function skip() {
    const next = skipTarget(items, selected?.id ?? null);
    if (next !== null && next !== selected?.id) void setPeek(next);
  }

  // The workspace owns j/k/s while it is on screen. The listener sits on the document
  // because the queue has nothing focused to hang a handler on — the chat does, and
  // the composer's Enter stays its own.
  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      const action = inboxKeyAction({
        key: e.key,
        ctrlKey: e.ctrlKey,
        metaKey: e.metaKey,
        altKey: e.altKey,
        isComposing: e.isComposing,
        targetTag: (e.target as HTMLElement | null)?.tagName,
        targetEditable: (e.target as HTMLElement | null)?.isContentEditable,
        layerOpen: hasOpenLayer(document),
      });
      if (action === null) return;
      if (action === "skip") {
        skip();
        return;
      }
      if (!selected) return;
      const to =
        action === "next"
          ? nextIssueId(items, selected.id)
          : prevIssueId(items, selected.id);
      if (to !== null) void setPeek(to);
    }
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [items, selected]);

  // New issue opens a fresh Draft at the head of the queue and selects it; the
  // composer beside it starts the authoring session on the first message.
  function openDraft() {
    setNewDraft(true);
    void setPeek(NEW_DRAFT_ID);
  }

  // A started Draft settles into a live authoring row keyed by its session id.
  // Selecting it retires the fresh Draft (the evaporation effect) with the interview
  // already in view.
  function onDraftStarted(session: GrillSession) {
    void setPeek(draftItemId(session.id));
  }

  // An applied outcome drops the issue's triage labels on the tracker, so refreshing
  // the board is what retires the row; the applied list is what re-lists it under
  // Done today. The issue's own grill list is left to its poll — the open thread
  // rides out the settle on the streamed session. A draft has no board row, so its
  // list is refreshed to retire the settled authoring row. A create apply raises the
  // created toast — the skip advances the queue, so the toast is what carries the
  // filed issue's id across the navigation.
  function onApplied(applied: GrillAppliedOutcome) {
    const wasDraft = selected?.draft;
    skip();
    void queryClient.invalidateQueries({ queryKey: ["backlog", repo] });
    void queryClient.invalidateQueries({
      queryKey: ["grill", repo, "applied"],
    });
    if (wasDraft)
      void queryClient.invalidateQueries({ queryKey: ["grill", repo] });
    if (applied.disposition === "create" && applied.issueId !== "")
      setCreated(applied);
  }

  // Discarding an authoring draft abandons its session with nothing to file, so the
  // list is refreshed to retire the row. An issue's discard just moves on — the issue
  // stays in the queue as untouched.
  function onDiscarded() {
    const wasDraft = selected?.draft;
    skip();
    if (wasDraft)
      void queryClient.invalidateQueries({ queryKey: ["grill", repo] });
  }

  return (
    <ProjectScopeGate
      className="min-h-0 flex-1"
      action="interview unclear issues"
    >
      {/* Both gates already establish a positioned wrapper, so filling it is how this
          page claims the height the root column left it — the recap banner above is
          conditional, and a fixed viewport offset would be wrong whenever it shows. */}
      <div className="absolute inset-0 flex flex-col">
        <PageHeader
          className="shrink-0"
          eyebrow={repo || "inbox"}
          title="Inbox"
          description="Make unclear or new work ready to run — questions waiting on you come first."
          actions={
            <div className="flex items-center gap-2">
              <button
                type="button"
                onClick={openDraft}
                className="inline-flex items-center gap-1.5 rounded-md border border-border px-3 py-1.5 text-sm text-foreground transition-colors hover:bg-muted"
              >
                <Sparkles className="size-4" />
                New issue
              </button>
              {untouchedIds.length > 0 && (
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => pregrillAll.mutate()}
                  disabled={pregrillAll.isPending}
                >
                  <Clock />
                  {pregrillAll.isPending
                    ? "Asking ahead…"
                    : `Ask ahead (${untouchedIds.length})`}
                </Button>
              )}
            </div>
          }
        />

        {(passSummary || pregrillAll.error) && (
          <p
            className={cn(
              "shrink-0 border-b border-border px-8 py-2 text-sm",
              pregrillAll.error ? "text-destructive" : "text-muted-foreground",
            )}
          >
            {pregrillAll.error ? pregrillAll.error.message : passSummary}
          </p>
        )}

        <RepoHealthGate className="min-h-0 flex-1">
          <div
            className={cn(
              "absolute inset-0 flex flex-col px-8 pb-4 md:grid md:grid-cols-[260px_minmax(0,1fr)]",
              contextOpen &&
                showContext &&
                "xl:[grid-template-columns:260px_minmax(0,1fr)_340px]",
            )}
          >
            <QueueSelect
              groups={groups}
              selectedId={selected?.id ?? null}
              onSelect={(id) => void setPeek(id)}
            />
            <QueueRail
              repo={repo}
              groups={groups}
              seen={seen}
              live={live?.session ?? null}
              selectedId={selected?.id ?? null}
              onSelect={(id) => void setPeek(id)}
            />

            <section
              aria-label="Interview"
              className="flex min-h-0 min-w-0 flex-col"
            >
              {selected ? (
                selected.attention === "done" && selected.session ? (
                  <DoneColumn
                    key={selected.id}
                    repo={repo}
                    item={selected}
                    session={selected.session}
                    status={live}
                    onStatus={setStatus}
                    contextOpen={contextOpen}
                    onToggleContext={toggleContext}
                  />
                ) : selected.draft ? (
                  <DraftColumn
                    key={selected.id}
                    repo={repo}
                    item={selected}
                    position={inboxPosition(items, selected.id)}
                    total={items.length}
                    status={live}
                    onStatus={setStatus}
                    onStarted={onDraftStarted}
                    onSkip={skip}
                    onApplied={onApplied}
                    onDiscarded={onDiscarded}
                  />
                ) : (
                  <SessionColumn
                    key={selected.id}
                    repo={repo}
                    item={selected}
                    position={inboxPosition(items, selected.id)}
                    total={items.length}
                    status={live}
                    hasSession={hasSession}
                    onStatus={setStatus}
                    contextOpen={contextOpen}
                    onToggleContext={toggleContext}
                    onSkip={skip}
                    onApplied={onApplied}
                    onDiscarded={onDiscarded}
                  />
                )
              ) : (
                <div className="flex min-h-0 flex-1 items-center justify-center p-8">
                  {error ? (
                    <ErrorNote message={error.message} />
                  ) : isLoading ? (
                    <p className="inline-flex items-center gap-2 text-sm text-muted-foreground">
                      <Loader2 className="size-4 animate-spin" />
                      Loading inbox…
                    </p>
                  ) : (
                    <EmptyInbox />
                  )}
                </div>
              )}
            </section>

            {selected && contextOpen && showContext && (
              <ContextColumn repo={repo} item={selected} status={live} />
            )}
          </div>
        </RepoHealthGate>
      </div>

      {created && (
        <CreatedToast
          id={created.issueId}
          title={created.issueTitle}
          actionLabel="View in backlog"
          onView={() => {
            void navigate({
              to: "/backlog",
              search: { issue: created.issueId },
            });
            setCreated(null);
          }}
          onDismiss={() => setCreated(null)}
        />
      )}
    </ProjectScopeGate>
  );
}

// SessionColumn is the chat zone: the session bar over the issue's conversation, or —
// when the issue has no session yet — a read-only preview whose actions are the only
// way a session starts. Hosts key it on the issue so the thread and preview reset
// together, and selecting or skimming an issue never opens one.
function SessionColumn({
  repo,
  item,
  position,
  total,
  status,
  hasSession,
  onStatus,
  contextOpen,
  onToggleContext,
  onSkip,
  onApplied,
  onDiscarded,
}: {
  repo: string;
  item: InboxItem;
  position: number;
  total: number;
  status: GrillStatus | null;
  hasSession: boolean;
  onStatus: (status: GrillStatus) => void;
  contextOpen: boolean;
  onToggleContext: () => void;
  onSkip: () => void;
  onApplied: (applied: GrillAppliedOutcome) => void;
  onDiscarded: () => void;
}) {
  const {
    session,
    resolved,
    starting,
    restarting,
    ending,
    error,
    endError,
    start,
    startOver,
    end,
    retry,
  } = useGrillSession(repo, item.id);

  // The stream's session outranks the list's: it is the one the thread is following.
  const live = status?.session ?? session;

  // The list decides which session mounts, but once the poll reads the open session
  // as settled the streamed copy keeps the thread on screen — dropping to a preview
  // would strand the just-finished conversation.
  const mounted = session ?? status?.session;

  return (
    <>
      <SessionBar
        item={item}
        position={position}
        total={total}
        session={live ?? null}
        pill={live ? inboxPill(live.state) : null}
        reconnecting={status?.stream === "error"}
        showContextToggle={hasSession}
        contextOpen={contextOpen}
        onToggleContext={onToggleContext}
        onSkip={onSkip}
        onStartOver={live ? startOver : undefined}
        restarting={restarting}
        onEnd={
          live && !isSettled(live.state) ? () => end(onDiscarded) : undefined
        }
        ending={ending}
        endError={endError}
      />

      {/* The overlay frame floats over the thread, not the bar above it: the bar
          carries the toggle that dismisses it again. */}
      <div className="relative flex min-h-0 flex-1 flex-col">
        {mounted ? (
          <GrillConversation
            key={mounted.id}
            repo={repo}
            initial={mounted}
            onStatus={onStatus}
            onApplied={onApplied}
            onDiscarded={onDiscarded}
          />
        ) : starting ? (
          <SpinnerNote label="Starting interview…" />
        ) : error ? (
          <div className="flex min-h-0 flex-1 items-center justify-center px-4">
            <div className="flex flex-col items-center gap-3">
              <ErrorNote message={error.message} />
              {retry && (
                <Button size="sm" variant="outline" onClick={retry}>
                  Try again
                </Button>
              )}
            </div>
          </div>
        ) : !resolved ? (
          <SpinnerNote label="Loading…" />
        ) : (
          <SessionPreview
            repo={repo}
            item={item}
            onStart={start}
            onSkip={onSkip}
          />
        )}

        {contextOpen && hasSession && (
          <ContextOverlay repo={repo} item={item} status={status} />
        )}
      </div>
    </>
  );
}

function SpinnerNote({ label }: { label: string }) {
  return (
    <div className="flex min-h-0 flex-1 items-center justify-center px-4">
      <p className="inline-flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="size-4 animate-spin" />
        {label}
      </p>
    </div>
  );
}

// DraftColumn is the chat zone for an issue-less authoring row: a live session's
// conversation once one exists, otherwise a fresh Draft whose composer starts it.
// There is no tracker issue behind it, so no context panel and no Start over.
function DraftColumn({
  repo,
  item,
  position,
  total,
  status,
  onStatus,
  onStarted,
  onSkip,
  onApplied,
  onDiscarded,
}: {
  repo: string;
  item: InboxItem;
  position: number;
  total: number;
  status: GrillStatus | null;
  onStatus: (status: GrillStatus) => void;
  onStarted: (session: GrillSession) => void;
  onSkip: () => void;
  onApplied: (applied: GrillAppliedOutcome) => void;
  onDiscarded: () => void;
}) {
  const queryClient = useQueryClient();
  const session = item.session;
  const live = session ? (status?.session ?? session) : null;

  // Discard draft abandons the authoring session with nothing filed; the settled
  // session drops out of the queue on the refetch onDiscarded triggers.
  const discard = useMutation({
    mutationFn: (sid: string) => abandonGrill(sid),
    onSuccess: () => onDiscarded(),
    onError: () =>
      void queryClient.invalidateQueries({ queryKey: ["grill", repo] }),
  });

  return (
    <>
      <SessionBar
        item={item}
        position={position}
        total={total}
        draft
        session={live}
        pill={live ? inboxPill(live.state) : null}
        reconnecting={status?.stream === "error"}
        showContextToggle={false}
        contextOpen={false}
        onToggleContext={() => {}}
        onSkip={onSkip}
        onEnd={session ? () => discard.mutate(session.id) : undefined}
        ending={discard.isPending}
        endError={discard.error}
      />

      <div className="relative flex min-h-0 flex-1 flex-col">
        {session ? (
          <GrillConversation
            key={session.id}
            repo={repo}
            initial={session}
            onStatus={onStatus}
            onApplied={onApplied}
            onDiscarded={onDiscarded}
          />
        ) : (
          <FreshDraftBody repo={repo} onStarted={onStarted} />
        )}
      </div>
    </>
  );
}

// DoneColumn reopens a Done today row for reference: the applied session's thread —
// what was asked, and the outcome that was applied — with nothing left to answer.
// The session is settled, so the bar drops the walk-through chrome.
function DoneColumn({
  repo,
  item,
  session,
  status,
  onStatus,
  contextOpen,
  onToggleContext,
}: {
  repo: string;
  item: InboxItem;
  session: GrillSession;
  status: GrillStatus | null;
  onStatus: (status: GrillStatus) => void;
  contextOpen: boolean;
  onToggleContext: () => void;
}) {
  const pill = inboxPill(session.state);

  return (
    <>
      <div className="shrink-0 border-b border-border">
        <div className="flex items-center justify-between gap-3 py-3 pl-5 pr-1">
          <span className="flex min-w-0 items-center gap-2 text-sm font-medium text-foreground">
            <span className="font-mono text-muted-foreground">{item.id}</span>
            <span className="truncate">{item.title}</span>
          </span>
          <div className="flex shrink-0 items-center gap-2">
            <StatusPill state={pill.tone} label={pill.label} />
            <Button
              variant="ghost"
              size="icon"
              className="size-8"
              onClick={onToggleContext}
              aria-pressed={contextOpen}
              title={contextOpen ? "Hide issue context" : "Show issue context"}
            >
              {contextOpen ? <PanelRightClose /> : <PanelRightOpen />}
              <span className="sr-only">
                {contextOpen ? "Hide issue context" : "Show issue context"}
              </span>
            </Button>
          </div>
        </div>
      </div>

      <div className="relative flex min-h-0 flex-1 flex-col">
        <GrillConversation
          key={session.id}
          repo={repo}
          initial={session}
          onStatus={onStatus}
        />
        {contextOpen && (
          <ContextOverlay repo={repo} item={item} status={status} />
        )}
      </div>
    </>
  );
}

// FreshDraftBody is the empty thread of an untouched Draft: a focused composer whose
// first message starts the authoring session (startGrillSession with no issue,
// the text as its seed). Nothing exists server-side until then.
function FreshDraftBody({
  repo,
  onStarted,
}: {
  repo: string;
  onStarted: (session: GrillSession) => void;
}) {
  const queryClient = useQueryClient();
  const start = useMutation({
    mutationFn: (seed: string) => startGrillSession(repo, "", seed),
    onSuccess: (session) => {
      queryClient.setQueryData<GrillListResponse>(["grill", repo], (prev) =>
        prev
          ? {
              ...prev,
              sessions: [
                session,
                ...prev.sessions.filter((s) => s.id !== session.id),
              ],
            }
          : { repo, sessions: [session] },
      );
      onStarted(session);
    },
  });

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex min-h-0 flex-1 items-center justify-center px-6">
        <p className="max-w-sm text-balance text-center text-sm leading-relaxed text-muted-foreground">
          Describe the issue you want to create. Your first message starts an
          interview toward a fully-specified issue — nothing is filed until you
          review the proposal.
        </p>
      </div>
      <div className="flex flex-col gap-3 border-t border-border p-4">
        <Composer
          placeholder="Describe the issue…"
          disabled={start.isPending}
          submitting={start.isPending}
          onSend={(text) => start.mutate(text)}
          autoFocus
        />
        {start.error && <ErrorNote message={(start.error as Error).message} />}
      </div>
    </div>
  );
}

// SessionPreview is what a no-session item shows in place of the conversation: a
// read-only read of the issue over a footer whose actions are the only way an
// Interview begins. Start interview opens a blank one; the composer opens with the
// typed message as the first turn; Ask ahead parks just the opening question for
// later; Skip moves on untouched.
function SessionPreview({
  repo,
  item,
  onStart,
  onSkip,
}: {
  repo: string;
  item: InboxItem;
  onStart: (seed?: string) => void;
  onSkip: () => void;
}) {
  const queryClient = useQueryClient();
  const issue = useQuery(issueQueryOptions(repo, item.id));
  const labels = (item.entry?.labels ?? []).filter((l) =>
    GRILLABLE_LABELS.includes(l),
  );

  const askAhead = useMutation({
    mutationFn: () => pregrillIssues(repo, [item.id]),
    onSettled: () =>
      void queryClient.invalidateQueries({ queryKey: ["grill", repo] }),
  });

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="min-h-0 flex-1 overflow-y-auto px-5 py-6">
        <div className="mx-auto flex max-w-2xl flex-col gap-4">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-xs text-muted-foreground">
              {item.id}
            </span>
            {labels.map((label) => (
              <span
                key={label}
                className="inline-flex items-center rounded-full border border-warn/50 bg-warn/12 px-2 py-0.5 font-mono text-[0.65rem] text-warn"
              >
                {label}
              </span>
            ))}
          </div>
          <h2 className="text-balance text-lg font-semibold leading-snug text-foreground">
            {item.title}
          </h2>
          <Description
            markdown={issue.data?.description.trim() ?? ""}
            loading={issue.isLoading}
            error={(issue.error as Error) ?? null}
          />
        </div>
      </div>

      <div className="flex flex-col gap-3 border-t border-border p-4">
        <p className="text-xs text-muted-foreground">
          No interview yet — start one, or send a first message to open with it.
        </p>
        <div className="flex flex-wrap items-center gap-2">
          <Button size="sm" onClick={() => onStart()}>
            <Sparkles />
            Start interview
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={() => askAhead.mutate()}
            disabled={askAhead.isPending}
          >
            <Clock className={cn(askAhead.isPending && "animate-pulse")} />
            {askAhead.isPending ? "Asking ahead…" : "Ask ahead"}
          </Button>
          <Button size="sm" variant="ghost" onClick={onSkip}>
            <SkipForward />
            Skip
          </Button>
        </div>
        <Composer
          placeholder="Type your first message to start the interview…"
          disabled={askAhead.isPending}
          submitting={false}
          onSend={(text) => onStart(text)}
        />
        {askAhead.error && (
          <ErrorNote message={(askAhead.error as Error).message} />
        )}
      </div>
    </div>
  );
}

function SessionBar({
  item,
  position,
  total,
  session,
  pill,
  reconnecting,
  showContextToggle,
  contextOpen,
  onToggleContext,
  onSkip,
  onStartOver,
  restarting,
  onEnd,
  ending,
  endError,
  draft,
}: {
  item: InboxItem;
  position: number;
  total: number;
  session?: GrillSession | null;
  pill: {
    tone: "warn" | "active" | "verify" | "success" | "todo";
    label: string;
  } | null;
  reconnecting: boolean;
  showContextToggle: boolean;
  contextOpen: boolean;
  onToggleContext: () => void;
  onSkip: () => void;
  onStartOver?: () => void;
  restarting?: boolean;
  onEnd?: () => void;
  ending?: boolean;
  endError?: Error | null;
  draft?: boolean;
}) {
  const [modelError, setModelError] = useState<string | null>(null);

  return (
    <div className="shrink-0 border-b border-border">
      <div className="flex items-center justify-between gap-3 py-3 pl-5 pr-1">
        <div className="flex min-w-0 items-center gap-3">
          <span className="shrink-0 font-mono text-xs tabular-nums text-muted-foreground">
            {position + 1} of {total}
          </span>
          <span className="flex min-w-0 items-center gap-2 text-sm font-medium text-foreground">
            {draft ? (
              <DraftChip />
            ) : (
              <span className="font-mono text-muted-foreground">{item.id}</span>
            )}
            <span className="truncate">
              {draft ? item.title || "New draft" : item.title}
            </span>
          </span>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {session && <ModelSwitch session={session} onError={setModelError} />}
          {reconnecting && (
            <span className="inline-flex items-center gap-1 text-xs text-warn">
              <span aria-hidden="true">⚠</span>
              reconnecting…
            </span>
          )}
          {pill && <StatusPill state={pill.tone} label={pill.label} />}
          {onStartOver && (
            <StartOverButton onConfirm={onStartOver} pending={restarting} />
          )}
          {onEnd && (
            <EndButton draft={draft} onConfirm={onEnd} pending={ending} />
          )}
          <Button
            variant="outline"
            size="sm"
            onClick={onSkip}
            aria-label="Skip to next issue"
          >
            <SkipForward />
            Skip
          </Button>
          {showContextToggle && (
            <Button
              variant="ghost"
              size="icon"
              className="size-8"
              onClick={onToggleContext}
              aria-pressed={contextOpen}
              title={contextOpen ? "Hide issue context" : "Show issue context"}
            >
              {contextOpen ? <PanelRightClose /> : <PanelRightOpen />}
              <span className="sr-only">
                {contextOpen ? "Hide issue context" : "Show issue context"}
              </span>
            </Button>
          )}
        </div>
      </div>
      {endError && (
        <p className="px-5 pb-2 text-xs text-destructive">{endError.message}</p>
      )}
      {modelError && (
        <p className="px-5 pb-2 text-xs text-destructive">{modelError}</p>
      )}
    </div>
  );
}

// ModelSwitch is the bar's provider/model indicator: a compact `claude · <model>`
// trigger over the session's Claude catalog. A switch applies from the next agent
// turn and the SSE state frame updates the label, so nothing here is optimistic; a
// finished or settled session keeps the label but the menu disables.
function ModelSwitch({
  session,
  onError,
}: {
  session: GrillSession;
  onError: (message: string | null) => void;
}) {
  const switchModel = useMutation({
    mutationFn: (model: string) => switchGrillModel(session.id, model),
    onMutate: () => onError(null),
    onError: (err) => onError((err as Error).message),
  });
  const model = session.model ?? "";
  const options = session.model_options ?? [];

  return (
    <Select
      value={model}
      onValueChange={(next) => switchModel.mutate(next)}
      disabled={
        session.state === "finished" ||
        isSettled(session.state) ||
        options.length === 0 ||
        switchModel.isPending
      }
    >
      <SelectTrigger
        size="sm"
        className="h-7 gap-1 border-none bg-transparent px-2 font-mono text-xs text-muted-foreground shadow-none dark:bg-transparent"
        aria-label="Switch model"
      >
        {session.provider ?? "claude"} · {model || "default"}
      </SelectTrigger>
      <SelectContent align="end">
        {options.map((m) => (
          <SelectItem key={m} value={m} className="font-mono text-xs">
            {m}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

// Start over discards the live Interview and opens a fresh one on the same item. The
// confirm guards only the typed answers — nothing has been written to the tracker — so
// it is a lightweight popover, not a modal. Never call this "Reset": Reset is the
// destructive ticket action (branch delete + re-queue).
function StartOverButton({
  onConfirm,
  pending,
}: {
  onConfirm: () => void;
  pending?: boolean;
}) {
  const [open, setOpen] = useState(false);

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          variant="outline"
          size="sm"
          disabled={pending}
          aria-label="Start over"
        >
          <RotateCcw className={cn(pending && "animate-spin")} />
          Start over
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-64">
        <p className="text-sm font-medium text-foreground">
          Discard this interview and start over?
        </p>
        <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
          Your typed answers are lost. The ticket and its labels stay untouched.
        </p>
        <div className="mt-3 flex justify-end gap-2">
          <Button variant="ghost" size="sm" onClick={() => setOpen(false)}>
            Cancel
          </Button>
          <Button
            size="sm"
            onClick={() => {
              setOpen(false);
              onConfirm();
            }}
          >
            Start over
          </Button>
        </div>
      </PopoverContent>
    </Popover>
  );
}

// End interview / Discard draft archives the live conversation without opening
// another: the session settles as abandoned and nothing is written to the tracker.
// An issue keeps its place in the queue as untouched; a draft row leaves it
// entirely. Same lightweight confirm as Start over — never "Delete" or "Archive".
function EndButton({
  draft,
  onConfirm,
  pending,
}: {
  draft?: boolean;
  onConfirm: () => void;
  pending?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const label = draft ? "Discard draft" : "End interview";

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          variant="outline"
          size="sm"
          disabled={pending}
          aria-label={label}
        >
          {pending ? (
            <Loader2 className="animate-spin" />
          ) : draft ? (
            <Trash2 />
          ) : (
            <Square />
          )}
          {label}
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-64">
        <p className="text-sm font-medium text-foreground">
          {draft ? "Discard this draft?" : "End this interview?"}
        </p>
        <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
          {draft
            ? "Nothing has been filed. The conversation is discarded."
            : "Your typed answers are lost. The ticket and its labels stay untouched — it returns to the queue as unstarted."}
        </p>
        <div className="mt-3 flex justify-end gap-2">
          <Button variant="ghost" size="sm" onClick={() => setOpen(false)}>
            Cancel
          </Button>
          <Button
            size="sm"
            onClick={() => {
              setOpen(false);
              onConfirm();
            }}
          >
            {label}
          </Button>
        </div>
      </PopoverContent>
    </Popover>
  );
}

function QueueRail({
  repo,
  groups,
  seen,
  live,
  selectedId,
  onSelect,
}: {
  repo: string;
  groups: InboxGroupView[];
  seen: SeenMarks;
  live: GrillSession | null;
  selectedId: string | null;
  onSelect: (id: string) => void;
}) {
  return (
    <nav
      aria-label="Triage queue"
      className="hidden min-h-0 flex-col gap-5 overflow-y-auto border-r border-border py-4 pr-3 md:flex"
    >
      {groups.map((group) => (
        <div key={group.group} className="flex flex-col gap-1.5">
          <div className="flex items-center justify-between px-2.5">
            <SectionLabel>{group.label}</SectionLabel>
            <span
              className={cn(
                "font-mono text-[0.65rem] tabular-nums",
                GROUP_COUNT_TONE[group.group],
              )}
            >
              {group.items.length}
            </span>
          </div>
          <ul className="flex flex-col gap-0.5">
            {group.items.map((item) =>
              item.attention === "done" ? (
                <DoneRow
                  key={item.id}
                  item={item}
                  selected={selectedId === item.id}
                  onSelect={() => onSelect(item.id)}
                />
              ) : (
                <QueueRow
                  key={item.id}
                  repo={repo}
                  item={item}
                  live={live}
                  unread={hasUnseenQuestion(seen, item)}
                  selected={selectedId === item.id}
                  onSelect={() => onSelect(item.id)}
                />
              ),
            )}
            {group.items.length === 0 && (
              <li className="px-2.5 py-1 font-mono text-xs text-faint">none</li>
            )}
          </ul>
        </div>
      ))}

      <div className="mt-auto flex flex-col gap-1 px-2.5 pt-4">
        <SectionLabel>Keys</SectionLabel>
        <p className="font-mono text-[0.65rem] leading-relaxed text-faint">
          j / k — next / prev · s — skip · enter — send
        </p>
      </div>
    </nav>
  );
}

function QueueRow({
  repo,
  item,
  live,
  unread,
  selected,
  onSelect,
}: {
  repo: string;
  item: InboxItem;
  live: GrillSession | null;
  unread: boolean;
  selected: boolean;
  onSelect: () => void;
}) {
  // The row's pill answers "what is this conversation doing right now" without
  // opening it; an untouched item has no conversation to report on.
  const session = rowSession(item, live);
  const pill = session ? inboxPill(session.state) : null;
  return (
    <li className="group/row relative">
      <button
        type="button"
        onClick={onSelect}
        aria-current={selected ? "true" : undefined}
        aria-label={item.draft ? "Open draft" : `Open ${item.id}`}
        className={cn(
          "flex w-full items-center gap-2 rounded-md px-2.5 py-2 text-left transition-colors",
          selected ? "bg-primary/10" : "hover:bg-secondary",
          item.attention === "open" && "pr-9",
        )}
      >
        {selected && (
          <span
            aria-hidden="true"
            className="absolute inset-y-2 left-0 w-0.5 rounded-full bg-primary"
          />
        )}
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="flex items-center gap-2">
            {item.draft ? (
              <DraftChip />
            ) : (
              <span
                className={cn(
                  "font-mono text-xs",
                  selected ? "text-primary" : "text-muted-foreground",
                )}
              >
                {item.id}
              </span>
            )}
            {unread && (
              <span
                className="size-1.5 rounded-full bg-warn"
                aria-hidden="true"
                title="A question you haven't read yet"
              />
            )}
            {pill && (
              <span
                className={cn(
                  "ml-auto shrink-0 font-mono text-[0.65rem]",
                  PILL_TEXT_TONE[pill.tone],
                )}
              >
                {pill.label}
              </span>
            )}
          </span>
          <span
            className={cn(
              "line-clamp-2 text-xs leading-relaxed",
              selected ? "text-foreground" : "text-muted-foreground",
            )}
          >
            {item.draft ? item.title || "New draft" : item.title}
          </span>
        </span>
        {item.assignee && (
          <AssigneeAvatar
            assignee={item.assignee}
            className="size-5 self-center text-[0.55rem]"
          />
        )}
      </button>
      {item.attention === "open" && !item.draft && (
        <PregrillButton
          repo={repo}
          issueId={item.id}
          className="absolute right-1 top-1 opacity-0 focus-visible:opacity-100 group-hover/row:opacity-100"
        />
      )}
    </li>
  );
}

// DraftChip stands in for an id on an issue-less authoring row — the queue's mark
// that nothing is filed yet.
function DraftChip() {
  return (
    <span className="inline-flex items-center rounded-full border border-primary/40 bg-primary/5 px-1.5 py-0.5 font-mono text-[0.65rem] uppercase tracking-wide text-primary">
      draft
    </span>
  );
}

// DoneRow stays openable after the day's triage: selecting it reopens the applied
// session read-only, for the history and the outcome that was applied.
function DoneRow({
  item,
  selected,
  onSelect,
}: {
  item: InboxItem;
  selected: boolean;
  onSelect: () => void;
}) {
  return (
    <li className="relative">
      <button
        type="button"
        onClick={onSelect}
        aria-current={selected ? "true" : undefined}
        aria-label={`Open ${item.id}`}
        className={cn(
          "flex w-full flex-col gap-0.5 rounded-md px-2.5 py-2 text-left transition-colors",
          selected
            ? "bg-primary/10"
            : "opacity-60 hover:bg-secondary hover:opacity-100",
        )}
      >
        {selected && (
          <span
            aria-hidden="true"
            className="absolute inset-y-2 left-0 w-0.5 rounded-full bg-primary"
          />
        )}
        <span className="inline-flex items-center gap-2 font-mono text-xs text-done">
          <span aria-hidden="true">✓</span>
          {item.id}
        </span>
        <span className="line-clamp-1 text-xs leading-relaxed text-muted-foreground">
          {item.title}
        </span>
      </button>
    </li>
  );
}

// QueueSelect is the rail's fallback under md, where 260px of chrome would crowd out
// the chat. All three groups are offered — a Done today row opens its applied
// session read-only, same as the rail's.
function QueueSelect({
  groups,
  selectedId,
  onSelect,
}: {
  groups: InboxGroupView[];
  selectedId: string | null;
  onSelect: (id: string) => void;
}) {
  return (
    <label className="flex shrink-0 flex-col gap-1 py-3 md:hidden">
      <span className="sr-only">Triage queue</span>
      <select
        value={selectedId ?? ""}
        onChange={(e) => onSelect(e.target.value)}
        className="h-9 w-full rounded-md border bg-transparent px-2 text-sm text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
      >
        {groups.map((group) => (
          <optgroup
            key={group.group}
            label={`${group.label} (${group.items.length})`}
          >
            {group.items.map((item) => (
              <option key={item.id} value={item.id}>
                {item.draft
                  ? `draft — ${item.title || "New draft"}`
                  : `${item.id} — ${item.title}`}
              </option>
            ))}
          </optgroup>
        ))}
      </select>
    </label>
  );
}

// The context frames are what the triager keeps an eye on while grilling: the issue
// as it stands today, and the proposal building up to replace it. Only one is ever
// on screen — the workspace's third column where there is room for it, an overlay
// over the chat where there is not.
function ContextColumn({
  repo,
  item,
  status,
}: {
  repo: string;
  item: InboxItem;
  status: GrillStatus | null;
}) {
  return (
    <aside
      aria-label="Issue context"
      className="hidden min-h-0 flex-col gap-5 overflow-y-auto border-l border-border py-4 pl-4 xl:flex"
    >
      <ContextBody repo={repo} item={item} status={status} />
    </aside>
  );
}

function ContextOverlay({
  repo,
  item,
  status,
}: {
  repo: string;
  item: InboxItem;
  status: GrillStatus | null;
}) {
  return (
    <aside
      aria-label="Issue context"
      className="absolute inset-y-0 right-0 z-10 flex w-[340px] max-w-full flex-col gap-5 overflow-y-auto rounded-lg border border-border bg-card p-4 shadow-lg xl:hidden"
    >
      <ContextBody repo={repo} item={item} status={status} />
    </aside>
  );
}

function ContextBody({
  repo,
  item,
  status,
}: {
  repo: string;
  item: InboxItem;
  status: GrillStatus | null;
}) {
  const issue = useQuery(issueQueryOptions(repo, item.id));
  const labels = (item.entry?.labels ?? []).filter((l) =>
    GRILLABLE_LABELS.includes(l),
  );
  const messages = status?.messages ?? [];
  const applied = status?.session.state === "applied";
  const outcome =
    status?.session.state === "finished" || applied
      ? latestOutcome(messages)
      : null;
  const rows = contextRows({
    created: issue.data?.created_at,
    source: item.entry?.source,
    messages,
    now: new Date(),
  });

  return (
    <>
      <div className="flex flex-col gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-mono text-sm text-foreground">{item.id}</span>
          {labels.map((label) => (
            <span
              key={label}
              className="inline-flex items-center rounded-full border border-warn/50 bg-warn/12 px-2 py-0.5 font-mono text-[0.65rem] text-warn"
            >
              {label}
            </span>
          ))}
        </div>
        <h2 className="text-balance text-sm font-semibold leading-relaxed text-foreground">
          {item.title}
        </h2>
      </div>

      <details open>
        <summary className="w-fit cursor-pointer font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground transition-colors hover:text-foreground">
          Description
        </summary>
        <div className="mt-2">
          <Description
            markdown={issue.data?.description.trim() ?? ""}
            loading={issue.isLoading}
            error={(issue.error as Error) ?? null}
          />
        </div>
      </details>

      <dl className="flex flex-col gap-2 border-t border-border pt-4">
        {rows.map((row) => (
          <div
            key={row.label}
            className="flex items-baseline justify-between gap-3"
          >
            <dt className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
              {row.label}
            </dt>
            <dd className="text-right font-mono text-xs text-foreground">
              {row.value}
            </dd>
          </div>
        ))}
      </dl>

      <div className="flex flex-col gap-1.5 border-t border-border pt-4">
        <SectionLabel>
          {applied ? "Applied outcome" : "Proposed outcome"}
        </SectionLabel>
        {outcome ? (
          <OutcomeMirror outcome={outcomePayload(outcome)} />
        ) : (
          <p className="text-xs leading-relaxed text-faint">
            Builds up as the session progresses — the updated ticket and draft
            sub-issues will appear here before anything is written back.
          </p>
        )}
      </div>
    </>
  );
}

function Description({
  markdown,
  loading,
  error,
}: {
  markdown: string;
  loading: boolean;
  error: Error | null;
}) {
  if (loading) {
    return (
      <p className="inline-flex items-center gap-2 text-xs text-muted-foreground">
        <Loader2 className="size-3 animate-spin" />
        Loading…
      </p>
    );
  }
  if (error) return <ErrorNote message={error.message} />;
  if (!markdown) return <p className="text-xs text-faint">No description.</p>;
  return <Markdown className="text-xs leading-relaxed">{markdown}</Markdown>;
}

// OutcomeMirror is read-only on purpose: the proposal is edited and approved in the
// chat column, and two live editors over one outcome is a conflict, not a feature.
function OutcomeMirror({ outcome }: { outcome: OutcomePayload }) {
  return (
    <ul className="flex flex-col gap-1.5">
      <li className="text-xs leading-relaxed text-foreground">
        {outcome.summary}
      </li>
      {outcome.sub_issues?.map((sub) => (
        <li
          key={sub.title}
          className="flex items-start gap-2 text-xs leading-relaxed text-muted-foreground"
        >
          <span className="text-faint" aria-hidden="true">
            ○
          </span>
          {sub.title}
        </li>
      ))}
    </ul>
  );
}

function EmptyInbox() {
  return (
    <div className="flex flex-col items-center gap-3">
      <span className="font-mono text-2xl text-done" aria-hidden="true">
        ✓
      </span>
      <p className="text-sm font-medium text-foreground">
        Nothing needs triage
      </p>
      <p className="max-w-sm text-center text-xs leading-relaxed text-muted-foreground">
        New issues labelled{" "}
        <span className="font-mono text-warn">needs-triage</span> land here
        automatically. Come back when the picker flags something unclear.
      </p>
    </div>
  );
}

function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <p className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
      {children}
    </p>
  );
}

function PregrillButton({
  repo,
  issueId,
  className,
}: {
  repo: string;
  issueId: string;
  className?: string;
}) {
  const queryClient = useQueryClient();
  const pregrill = useMutation({
    mutationFn: () => pregrillIssues(repo, [issueId]),
    onSettled: () =>
      void queryClient.invalidateQueries({ queryKey: ["grill", repo] }),
  });

  return (
    <Button
      variant="ghost"
      size="icon"
      className={cn("size-7 shrink-0", className)}
      onClick={() => pregrill.mutate()}
      disabled={pregrill.isPending}
      aria-label={`Ask ahead ${issueId}`}
      title="Ask ahead — have the opening question waiting for you"
    >
      <Clock className={cn(pregrill.isPending && "animate-pulse")} />
    </Button>
  );
}
