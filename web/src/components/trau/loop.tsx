import { useEffect, useReducer, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  ArrowDown,
  ArrowRight,
  ArrowUp,
  Check,
  ChevronDown,
  ChevronRight,
  ExternalLink,
  Info,
  ListPlus,
  Plus,
  Power,
  RefreshCw,
  Search,
  Square,
  SquareTerminal,
  TriangleAlert,
  X,
} from "lucide-react";
import { parseAsString, useQueryState } from "nuqs";

import { Button } from "@/components/ui/button";
import { IssueDrawer } from "@/components/issue-drawer";
import { MakeStartableButton } from "@/components/make-startable-button";
import { useActiveRepo } from "@/components/trau/active-repo";
import { AddTicketDialog } from "@/components/trau/add-ticket-dialog";
import { RepoPicker } from "@/components/trau/repo-picker";
import { TargetRepoField } from "@/components/trau/target-repo-field";
import { ConfirmDialog } from "@/components/trau/confirm-dialog";
import { EmptyState } from "@/components/trau/empty-state";
import { Eyebrow } from "@/components/trau/eyebrow";
import { useHandback } from "@/components/trau/handback-dialog";
import { PhaseStepper } from "@/components/trau/phase-stepper";
import { SegmentedControl } from "@/components/trau/segmented-control";
import { StatusPill, type RunState } from "@/components/trau/status-pill";
import { TerminalCard } from "@/components/trau/terminal-card";
import { cn } from "@/lib/utils";
import {
  addByIdState,
  pendingBehind,
  runNextCopy,
  statusWarning,
} from "@/lib/add-by-id";
import { configQueryOptions } from "@/lib/config";
import { addAllLabel, eligibleQueryOptions, planAddAll } from "@/lib/eligible";
import { pendingHandback } from "@/lib/handback";
import { IssueFetchError, issueQueryOptions } from "@/lib/issues";
import {
  instancesQueryOptions,
  type Instance,
  type RepoFreshness,
} from "@/lib/instances";
import {
  isTakeover,
  projectLoopState,
  repoInstance,
  type LoopHalt,
  type LoopView,
} from "@/lib/loop";
import { loopTitle, usePageTitle, type LoopTitleState } from "@/lib/page-title";
import {
  dequeue,
  drain,
  enqueue,
  moveQueueItem,
  publishQueue,
  queueExecutable,
  queueQueryOptions,
  runNext as runNextRequest,
  shutdownQueue,
  skipResumeApplies,
  type OnFault,
  type QueueItem,
  type QueueResponse,
} from "@/lib/queue";
import {
  pauseKind,
  runSteps,
  STOPPED_HEADLINE,
  STOPPED_HINT,
} from "@/lib/runlive";
import { stepName } from "@/lib/steps";
import { runsQueryOptions } from "@/lib/runs";
import {
  builderView,
  finishedReducer,
  finishedView,
  ticketPill,
  FINISHED_INITIAL,
  FINISHED_PAGE_SIZE,
  type PendingEntry,
  type Timeline,
  type TimelineTicket,
} from "@/lib/timeline";

const NO_OVERRIDE = "default";

function actionError(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function shutdownDescription(queuedCount: number): string {
  const noun = queuedCount === 1 ? "item" : "items";
  return `Stops the running task if any (force-killed if it hasn't exited after ~30s), removes all ${queuedCount} queued ${noun}, and clears paused run leftovers. Work in progress on feature branches is kept; tracker tickets are not changed.`;
}

// ShutdownAction is the destructive teardown gesture shared by both Loop card
// shapes: it stays hidden until there is something to tear down (a queued
// item or a live child), then disables itself and reads "Shutting down…"
// until the hub confirms the teardown and the queue query clears.
function ShutdownAction({
  repo,
  queuedCount,
  hasRunningChild,
  shuttingDown,
  onConfirm,
  error,
}: {
  repo: string;
  queuedCount: number;
  hasRunningChild: boolean;
  shuttingDown: boolean;
  onConfirm: () => void;
  error: unknown;
}) {
  if (!shuttingDown && queuedCount === 0 && !hasRunningChild) return null;

  return (
    <div className="flex flex-col items-end gap-2">
      {error ? (
        <p className="font-mono text-xs text-destructive">
          {actionError(error)}
        </p>
      ) : null}
      <ConfirmDialog
        windowTitle="confirm"
        trigger={
          <Button
            variant="destructive"
            size="sm"
            className="font-mono"
            disabled={shuttingDown}
          >
            <Power className="size-4" aria-hidden="true" />
            {shuttingDown ? "Shutting down…" : "Shut down"}
          </Button>
        }
        title={`Shut down the loop on ${repo}?`}
        description={shutdownDescription(queuedCount)}
        confirmLabel="Shut down"
        destructive
        onConfirm={onConfirm}
      />
    </div>
  );
}

function useNow(intervalMs: number): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);
  return now;
}

function elapsedSince(fromISO: string, now: number): string {
  const s = Math.max(0, Math.floor((now - new Date(fromISO).getTime()) / 1000));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  if (h > 0) return `${h}h ${String(m).padStart(2, "0")}m`;
  return `${m}m ${String(sec).padStart(2, "0")}s`;
}

// SyncFreshness shows the issue store's synced-ness for the loop card: a spinner
// while a background sync runs, the last-synced age once it lands, or a warning
// when the last sync failed. It stays silent for a repo that has never synced, so
// a repo with no tracker shows nothing rather than a misleading state.
function SyncFreshness({ freshness }: { freshness?: RepoFreshness }) {
  const now = useNow(30_000);
  if (!freshness) return null;
  if (freshness.syncing) {
    return (
      <span className="inline-flex items-center gap-1.5 font-mono text-xs text-muted-foreground">
        <RefreshCw className="size-3 animate-spin" aria-hidden="true" />
        syncing…
      </span>
    );
  }
  if (freshness.last_error) {
    return (
      <span
        className="inline-flex items-center gap-1.5 font-mono text-xs text-warn"
        title={freshness.last_error}
      >
        <TriangleAlert className="size-3" aria-hidden="true" />
        sync failed
      </span>
    );
  }
  if (!freshness.last_synced_at) return null;
  return (
    <span className="inline-flex items-center gap-1.5 font-mono text-xs text-muted-foreground">
      <Check className="size-3 text-done" aria-hidden="true" />
      synced {elapsedSince(freshness.last_synced_at, now)} ago
    </span>
  );
}

const STATUS_STATE: Record<string, RunState> = {
  pending: "todo",
  running: "active",
  paused: "warn",
  done: "success",
  failed: "fail",
  skipped: "info",
};

function statusState(status: string): RunState {
  return STATUS_STATE[status] ?? "info";
}

const SUB_GLYPH: Record<
  string,
  { glyph: string; className: string; label: string }
> = {
  done: { glyph: "✓", className: "text-done", label: "done" },
  epic: { glyph: "◆", className: "text-info", label: "epic" },
  todo: { glyph: "○", className: "text-faint", label: "todo" },
};

function subGlyph(state: string) {
  return SUB_GLYPH[state] ?? SUB_GLYPH.todo;
}

// InternalTag marks a row the tracker knows nothing about, so a queue mixing both
// reads unambiguously. A synced row stays unmarked — it is the common case.
function InternalTag({ source }: { source?: string }) {
  if (source !== "internal") return null;
  return (
    <span className="shrink-0 rounded-sm border border-border bg-secondary/60 px-1.5 py-0.5 font-mono text-[0.6rem] uppercase tracking-[0.14em] text-muted-foreground">
      internal
    </span>
  );
}

// ProviderTag surfaces a per-item provider override, so a queued run that will
// not use the configured routing says so on its row.
function ProviderTag({ provider }: { provider?: string }) {
  if (!provider) return null;
  return (
    <span
      title="provider · this run only"
      className="shrink-0 rounded-sm border border-border bg-secondary/60 px-1.5 py-0.5 font-mono text-[0.6rem] text-muted-foreground"
    >
      {provider}
    </span>
  );
}

// TicketIdButton is the drawer trigger: only the id text is clickable, so it
// never competes with row-level links or the builder's reorder controls.
function TicketIdButton({
  id,
  onPeek,
  className,
}: {
  id: string;
  onPeek: (id: string) => void;
  className?: string;
}) {
  return (
    <button
      type="button"
      onClick={(e) => {
        e.preventDefault();
        e.stopPropagation();
        onPeek(id);
      }}
      aria-label={`Open ${id}`}
      className={cn(
        "shrink-0 font-mono underline-offset-4 hover:underline",
        className,
      )}
    >
      {id}
    </button>
  );
}

function epicCounts(item: QueueItem): { done: number; total: number } {
  const subs = item.sub_issues ?? [];
  return {
    done: subs.filter((s) => s.state === "done").length,
    total: subs.length,
  };
}

function SkipResumeToggle({
  value,
  onChange,
}: {
  value: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex items-center gap-3">
        <button
          type="button"
          role="switch"
          aria-checked={value}
          aria-label="Skip resume"
          onClick={() => onChange(!value)}
          className={cn(
            "relative inline-flex h-5 w-9 shrink-0 items-center rounded-full border transition-colors",
            value ? "border-primary bg-primary/30" : "border-border bg-input",
          )}
        >
          <span
            aria-hidden="true"
            className={cn(
              "inline-block size-3.5 rounded-full transition-transform",
              value
                ? "translate-x-4 bg-primary"
                : "translate-x-0.5 bg-muted-foreground",
            )}
          />
        </button>
        <span className="font-mono text-sm text-foreground">skip resume</span>
      </div>
      <p className="font-sans text-xs leading-relaxed text-muted-foreground">
        This queue has prior progress. Start fresh from the top; ignore stored
        checkpoints.
      </p>
    </div>
  );
}

const ON_FAULT_OPTIONS: { value: OnFault; label: string }[] = [
  { value: "halt", label: "Halt" },
  { value: "skip", label: "Skip & continue" },
];

function OnFaultToggle({
  value,
  onChange,
}: {
  value: OnFault;
  onChange: (v: OnFault) => void;
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <span className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
        on fault
      </span>
      <SegmentedControl
        aria-label="On fault"
        options={ON_FAULT_OPTIONS}
        value={value}
        onChange={onChange}
      />
      <p className="font-sans text-xs leading-relaxed text-muted-foreground">
        {value === "halt"
          ? "A fault parks the queue for you to intervene."
          : "A fault settles the item failed and the queue drains on. Queue order is not dependency-aware: items that depend on a skipped ticket may fail."}
      </p>
    </div>
  );
}

function QueueBuilderRow({
  item,
  index,
  count,
  expanded,
  busy,
  onToggle,
  onMove,
  onRemove,
  onPeek,
}: {
  item: QueueItem;
  index: number;
  count: number;
  expanded: boolean;
  busy: boolean;
  onToggle: () => void;
  onMove: (dir: -1 | 1) => void;
  onRemove: () => void;
  onPeek: (id: string) => void;
}) {
  const isEpic = item.kind === "epic";
  const { done, total } = epicCounts(item);
  const subs = item.sub_issues ?? [];

  return (
    <li className="border-b border-border/60 last:border-0">
      <div className="flex items-center gap-3 px-3 py-2.5">
        <span className="w-5 shrink-0 text-right font-mono text-xs text-faint">
          {index + 1}
        </span>

        {isEpic ? (
          <button
            type="button"
            onClick={onToggle}
            aria-expanded={expanded}
            aria-label={expanded ? `Collapse ${item.id}` : `Expand ${item.id}`}
            className="shrink-0 text-muted-foreground transition-colors hover:text-foreground"
          >
            {expanded ? (
              <ChevronDown className="size-3.5" aria-hidden="true" />
            ) : (
              <ChevronRight className="size-3.5" aria-hidden="true" />
            )}
          </button>
        ) : (
          <span className="w-3.5 shrink-0" aria-hidden="true" />
        )}

        <TicketIdButton
          id={item.id}
          onPeek={onPeek}
          className="text-sm text-primary"
        />
        <span className="min-w-0 flex-1 truncate font-sans text-sm text-foreground">
          {item.title || "—"}
        </span>
        <InternalTag source={item.source} />
        <ProviderTag provider={item.provider} />

        {isEpic ? (
          <StatusPill state="info" label={`epic · ${done}/${total}`} />
        ) : item.status !== "pending" ? (
          <StatusPill state={statusState(item.status)} label={item.status} />
        ) : (
          <StatusPill state="todo" label="ticket" />
        )}

        <div className="flex shrink-0 items-center gap-0.5">
          <button
            type="button"
            onClick={() => onMove(-1)}
            disabled={index === 0 || busy}
            aria-label={`Move ${item.id} up`}
            className="flex size-6 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground disabled:pointer-events-none disabled:opacity-30"
          >
            <ArrowUp className="size-3.5" aria-hidden="true" />
          </button>
          <button
            type="button"
            onClick={() => onMove(1)}
            disabled={index === count - 1 || busy}
            aria-label={`Move ${item.id} down`}
            className="flex size-6 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground disabled:pointer-events-none disabled:opacity-30"
          >
            <ArrowDown className="size-3.5" aria-hidden="true" />
          </button>
          <button
            type="button"
            onClick={onRemove}
            disabled={busy}
            aria-label={`Remove ${item.id} from queue`}
            className="flex size-6 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-secondary hover:text-fail disabled:pointer-events-none disabled:opacity-30"
          >
            <X className="size-3.5" aria-hidden="true" />
          </button>
        </div>
      </div>

      {isEpic && expanded && subs.length > 0 && (
        <ul className="border-t border-border/60 bg-secondary/20">
          {subs.map((sub) => {
            const styles = subGlyph(sub.state);
            return (
              <li
                key={sub.id}
                className="flex items-center gap-3 border-b border-border/40 py-1.5 pl-14 pr-3 last:border-0"
              >
                <TicketIdButton
                  id={sub.id}
                  onPeek={onPeek}
                  className="text-xs text-primary/80"
                />
                <span className="min-w-0 flex-1 truncate font-sans text-xs text-muted-foreground">
                  {sub.title}
                </span>
                <span
                  className={cn(
                    "inline-flex shrink-0 items-center gap-1.5 font-mono text-xs",
                    styles.className,
                  )}
                >
                  <span aria-hidden="true">{styles.glyph}</span>
                  {styles.label}
                </span>
              </li>
            );
          })}
        </ul>
      )}
    </li>
  );
}

function LaunchQueueCard({
  repo,
  freshness,
  hasRunningChild,
  shuttingDown,
  onShutdown,
  shutdownError,
  onPeek,
}: {
  repo: string;
  freshness?: RepoFreshness;
  hasRunningChild: boolean;
  shuttingDown: boolean;
  onShutdown: () => void;
  shutdownError: unknown;
  onPeek: (id: string) => void;
}) {
  const queryClient = useQueryClient();
  const queue = useQuery(queueQueryOptions(repo));
  const eligible = useQuery(eligibleQueryOptions(repo));
  const runs = useQuery(runsQueryOptions(repo));

  const items = queue.data?.items ?? [];
  const builder = builderView(items, runs.data?.runs ?? []);
  const addAllPlan = planAddAll(eligible.data?.tickets ?? [], items);
  const skipResumeShown = skipResumeApplies(items, runs.data?.runs ?? []);
  const [draft, setDraft] = useState("");
  const [submittedId, setSubmittedId] = useState("");
  const [provider, setProvider] = useState(NO_OVERRIDE);
  const [expandedIds, setExpandedIds] = useState<string[]>([]);
  const [browseOpen, setBrowseOpen] = useState(false);
  const [skipResume, setSkipResume] = useState(false);
  const [onFault, setOnFault] = useState<OnFault>("halt");

  const config = useQuery(configQueryOptions(repo));
  const providers = [NO_OVERRIDE, ...(config.data?.providers ?? [])];
  const issue = useQuery(issueQueryOptions(repo, submittedId));
  const ticket = issue.data;
  const addState = addByIdState(submittedId, ticket, issue.error);
  const warning =
    ticket && !addState.wrongProject ? statusWarning(ticket) : null;
  const overrideProvider = provider === NO_OVERRIDE ? undefined : provider;

  const setQueue = (res: QueueResponse) => publishQueue(queryClient, repo, res);

  const resetAdd = () => {
    setDraft("");
    setSubmittedId("");
    setProvider(NO_OVERRIDE);
  };

  useEffect(() => {
    resetAdd();
  }, [repo]);

  const add = useMutation({
    mutationFn: () =>
      enqueue(repo, { id: submittedId, provider: overrideProvider }),
    onSuccess: (res) => {
      setQueue(res);
      resetAdd();
    },
  });

  // Run next is one gesture: land the ticket in the first pending slot, then arm
  // the drain. Landing is this page's timeline — the queue response flips the
  // view to running, never a live-page navigation.
  const runNext = useMutation({
    mutationFn: () =>
      runNextRequest(
        repo,
        { id: submittedId, provider: overrideProvider },
        { no_resume: skipResume && skipResumeShown, on_fault: onFault },
      ),
    onSuccess: (res) => {
      setQueue(res);
      resetAdd();
    },
  });
  const handback = useHandback(repo, () => runNext.mutate());

  const addAll = useMutation({
    mutationFn: async () => {
      const errors: string[] = [];
      for (const it of addAllPlan.items) {
        try {
          setQueue(await enqueue(repo, { id: it.id, kind: it.kind }));
        } catch (err) {
          errors.push(`${it.id}: ${actionError(err)}`);
        }
      }
      if (errors.length > 0) throw new Error(errors.join("\n"));
    },
  });

  const move = useMutation({
    mutationFn: (vars: { id: string; dir: -1 | 1 }) =>
      moveQueueItem(repo, vars.id, vars.dir),
    onSuccess: setQueue,
  });

  const remove = useMutation({
    mutationFn: (id: string) => dequeue(repo, id),
    onSuccess: setQueue,
  });

  const start = useMutation({
    mutationFn: () =>
      drain(repo, true, {
        no_resume: skipResume && skipResumeShown,
        on_fault: onFault,
      }),
    onSuccess: setQueue,
  });

  const executable = queueExecutable(builder.queue);

  const busy =
    move.isPending ||
    remove.isPending ||
    add.isPending ||
    addAll.isPending ||
    runNext.isPending ||
    shuttingDown;

  // The ticket is fetched for confirmation the moment the user commits an id —
  // on Enter or on blur — so there's no extra "fetch" click to reach the confirm.
  const fetchTicket = () => {
    const id = draft.trim().toUpperCase();
    if (id) setSubmittedId(id);
  };

  const toggleExpand = (id: string) =>
    setExpandedIds((prev) =>
      prev.includes(id) ? prev.filter((e) => e !== id) : [...prev, id],
    );

  return (
    <div className="flex max-w-3xl flex-col gap-6">
      <TerminalCard title="loop-launch">
        <form
          className="flex flex-col gap-6"
          onSubmit={(e) => e.preventDefault()}
        >
          <div className="flex flex-col gap-1.5">
            <div className="flex items-center justify-between gap-3">
              <label className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
                repo
              </label>
              <SyncFreshness freshness={freshness} />
            </div>
            <TargetRepoField repo={repo} />
          </div>

          <div className="flex flex-col gap-3">
            <div className="flex flex-col gap-1.5">
              <label
                htmlFor="queue-add"
                className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground"
              >
                queue
              </label>
              <div className="flex flex-wrap items-center gap-2">
                <input
                  id="queue-add"
                  value={draft}
                  onChange={(e) => {
                    setDraft(e.target.value);
                    if (
                      submittedId &&
                      e.target.value.trim().toUpperCase() !== submittedId
                    ) {
                      setSubmittedId("");
                    }
                    if (add.error) add.reset();
                    if (runNext.error) runNext.reset();
                  }}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && !e.nativeEvent.isComposing) {
                      e.preventDefault();
                      fetchTicket();
                    }
                  }}
                  onBlur={fetchTicket}
                  placeholder="COD-### (ticket or epic)"
                  autoComplete="off"
                  spellCheck={false}
                  disabled={shuttingDown}
                  className="w-56 rounded-md border border-border bg-input px-2.5 py-1.5 font-mono text-sm text-foreground placeholder:text-muted-foreground/60 focus-visible:border-ring focus-visible:outline-none"
                />
                <Button
                  type="button"
                  variant={addState.confirmed ? "outline" : "default"}
                  size="sm"
                  className="font-mono"
                  onClick={fetchTicket}
                  disabled={issue.isFetching || draft.trim() === "" || shuttingDown}
                >
                  {issue.isFetching ? "Fetching…" : "Fetch ticket"}
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="font-mono"
                  onClick={() => setBrowseOpen(true)}
                  disabled={shuttingDown}
                >
                  <Search className="size-4" aria-hidden="true" />
                  Browse…
                </Button>
                {addAllPlan.items.length > 0 && (
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    className="font-mono"
                    onClick={() => addAll.mutate()}
                    disabled={addAll.isPending || shuttingDown}
                  >
                    <ListPlus className="size-4" aria-hidden="true" />
                    {addAll.isPending ? "Adding…" : addAllLabel(addAllPlan)}
                  </Button>
                )}
              </div>
              <p className="font-sans text-xs leading-relaxed text-muted-foreground">
                Press Enter to fetch the ticket for confirmation before anything
                is queued. Epics are taken whole — all remaining sub-issues.
              </p>
              {addAll.error ? (
                <p className="font-mono text-xs text-fail" role="alert">
                  {actionError(addAll.error)}
                </p>
              ) : null}
              {move.error ? (
                <p className="font-mono text-xs text-fail" role="alert">
                  {actionError(move.error)}
                </p>
              ) : null}
              {remove.error ? (
                <p className="font-mono text-xs text-fail" role="alert">
                  {actionError(remove.error)}
                </p>
              ) : null}
            </div>

            {issue.isFetching && submittedId !== "" && (
              <div
                aria-busy="true"
                className="flex flex-col gap-2 rounded-md border border-border bg-secondary/30 px-3 py-3"
              >
                <div className="flex items-center gap-3">
                  <span className="h-3 w-16 animate-pulse rounded bg-muted" />
                  <span className="h-3 w-2/3 animate-pulse rounded bg-muted" />
                </div>
                <span className="h-3 w-24 animate-pulse rounded bg-muted" />
              </div>
            )}

            {!issue.isFetching && issue.error && (
              <FetchError error={issue.error} id={submittedId} />
            )}

            {addState.confirmed && ticket && (
              <div
                className={cn(
                  "flex flex-col gap-3 rounded-md border px-3 py-3",
                  addState.wrongProject
                    ? "border-fail/40 bg-fail/5"
                    : "border-primary/40 bg-primary/5",
                )}
              >
                <div className="flex flex-wrap items-center gap-2">
                  <span
                    className={cn(
                      "font-mono text-sm",
                      addState.wrongProject ? "text-fail" : "text-primary",
                    )}
                  >
                    {ticket.id}
                  </span>
                  <span className="font-sans text-sm text-foreground">
                    {ticket.title}
                  </span>
                  {ticket.has_children && (
                    <span className="inline-flex shrink-0 items-center gap-1 font-mono text-[0.7rem] text-info">
                      <span aria-hidden="true">◆</span>
                      epic · runs all remaining sub-issues
                    </span>
                  )}
                </div>
                <dl className="flex flex-wrap gap-x-6 gap-y-1 font-mono text-xs">
                  <div className="flex items-center gap-2">
                    <dt className="text-muted-foreground">status</dt>
                    <dd className="text-foreground">{ticket.status || "—"}</dd>
                  </div>
                  {ticket.project && (
                    <div className="flex items-center gap-2">
                      <dt className="text-muted-foreground">project</dt>
                      <dd
                        className={
                          addState.wrongProject
                            ? "text-fail"
                            : "text-foreground"
                        }
                      >
                        {ticket.project}
                      </dd>
                    </div>
                  )}
                  {ticket.labels.length > 0 && (
                    <div className="flex items-center gap-2">
                      <dt className="text-muted-foreground">labels</dt>
                      <dd className="flex flex-wrap gap-1.5">
                        {ticket.labels.map((label) => (
                          <span
                            key={label}
                            className="rounded border border-border bg-muted/60 px-1.5 py-0.5 text-muted-foreground"
                          >
                            {label}
                          </span>
                        ))}
                      </dd>
                    </div>
                  )}
                </dl>
                {addState.wrongProject ? (
                  <p
                    role="alert"
                    className="flex items-start gap-2 rounded-md border border-fail/40 bg-fail/5 px-2.5 py-2 font-sans text-xs leading-relaxed text-fail"
                  >
                    <TriangleAlert
                      className="mt-0.5 size-3.5 shrink-0"
                      aria-hidden="true"
                    />
                    <span>
                      {ticket.id} belongs to another project
                      {ticket.project ? ` (${ticket.project})` : ""}, not {repo}
                      . Switch to that project's repo to run it.
                    </span>
                  </p>
                ) : (
                  warning && (
                    <p
                      className={cn(
                        "flex items-start gap-2 rounded-md border px-2.5 py-2 font-sans text-xs leading-relaxed",
                        warning.tone === "warn"
                          ? "border-warn/40 bg-warn/5 text-warn"
                          : "border-border bg-secondary/40 text-muted-foreground",
                      )}
                    >
                      {warning.tone === "warn" ? (
                        <TriangleAlert
                          className="mt-0.5 size-3.5 shrink-0"
                          aria-hidden="true"
                        />
                      ) : (
                        <Info
                          className="mt-0.5 size-3.5 shrink-0"
                          aria-hidden="true"
                        />
                      )}
                      <span>{warning.text}</span>
                    </p>
                  )
                )}
              </div>
            )}

            {addState.canQueue && (
              <div className="flex flex-col gap-4 rounded-md border border-border bg-secondary/20 px-3 py-3">
                <div className="flex flex-col gap-1.5">
                  <RepoPicker
                    repos={providers}
                    value={provider}
                    onChange={setProvider}
                    label="provider · this run only"
                  />
                  <p className="font-sans text-xs leading-relaxed text-muted-foreground">
                    Reverts when the run ends.
                  </p>
                </div>
                <div className="flex flex-col gap-2">
                  <p className="flex items-center gap-2 font-mono text-xs text-muted-foreground">
                    <ArrowRight
                      className="size-3.5 text-teal"
                      aria-hidden="true"
                    />
                    <span>
                      {runNextCopy(
                        submittedId,
                        pendingBehind(items, submittedId),
                      )}
                    </span>
                  </p>
                  <div className="flex flex-wrap items-center gap-2">
                    <Button
                      type="button"
                      size="sm"
                      className="font-mono"
                      onClick={() =>
                        handback.request(
                          submittedId,
                          pendingHandback(runs.data?.runs, submittedId),
                        )
                      }
                      disabled={runNext.isPending || add.isPending || shuttingDown}
                    >
                      {runNext.isPending ? "Starting…" : "Run next"}
                    </Button>
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      className="font-mono"
                      onClick={() => add.mutate()}
                      disabled={add.isPending || runNext.isPending || shuttingDown}
                    >
                      <Plus className="size-4" aria-hidden="true" />
                      {add.isPending ? "Adding…" : "Add to queue"}
                    </Button>
                  </div>
                  {add.error ? (
                    <p className="font-mono text-xs text-fail" role="alert">
                      {actionError(add.error)}
                    </p>
                  ) : null}
                  {runNext.error ? (
                    <p className="font-mono text-xs text-fail" role="alert">
                      {actionError(runNext.error)}
                    </p>
                  ) : null}
                </div>
              </div>
            )}

            {builder.queue.length === 0 ? (
              <EmptyState
                message="Queue is empty — add a ticket or epic above to build your run."
                actions={
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    className="font-mono"
                    onClick={() => setBrowseOpen(true)}
                  >
                    <Search className="size-4" aria-hidden="true" />
                    Browse issues
                  </Button>
                }
              />
            ) : (
              <div className="overflow-hidden rounded-md border border-border">
                <ul className="flex flex-col">
                  {builder.queue.map((item, index) => (
                    <QueueBuilderRow
                      key={item.id}
                      item={item}
                      index={index}
                      count={builder.queue.length}
                      expanded={expandedIds.includes(item.id)}
                      busy={busy}
                      onToggle={() => toggleExpand(item.id)}
                      onMove={(dir) => move.mutate({ id: item.id, dir })}
                      onRemove={() => remove.mutate(item.id)}
                      onPeek={onPeek}
                    />
                  ))}
                </ul>
                <div className="border-t border-border bg-secondary/40 px-3 py-2 font-mono text-xs text-muted-foreground">
                  {builder.queue.length} queued · {executable} executable{" "}
                  {executable === 1 ? "ticket" : "tickets"} · runs top to bottom
                </div>
              </div>
            )}
          </div>

          <div className="flex flex-col gap-4 border-t border-border pt-4">
            <OnFaultToggle value={onFault} onChange={setOnFault} />
            {skipResumeShown ? (
              <SkipResumeToggle value={skipResume} onChange={setSkipResume} />
            ) : null}
          </div>

          <div className="flex flex-col gap-2 border-t border-border pt-4">
            <Button
              type="button"
              size="sm"
              className="w-fit font-mono"
              onClick={() => start.mutate()}
              disabled={executable === 0 || start.isPending || shuttingDown}
            >
              {start.isPending ? "Starting…" : "Start queue"}
            </Button>
            {start.error ? (
              <p className="font-mono text-xs text-destructive">
                {actionError(start.error)}
              </p>
            ) : null}
          </div>
        </form>

        <AddTicketDialog
          repo={repo}
          queued={items}
          open={browseOpen}
          onOpenChange={setBrowseOpen}
          onQueue={setQueue}
        />
      </TerminalCard>

      <ShutdownAction
        repo={repo}
        queuedCount={builder.queue.length}
        hasRunningChild={hasRunningChild}
        shuttingDown={shuttingDown}
        onConfirm={onShutdown}
        error={shutdownError}
      />

      {builder.settled.length > 0 ? (
        <FinishedSection
          repo={repo}
          settled={builder.settled}
          onPeek={onPeek}
        />
      ) : null}

      {handback.dialog}
    </div>
  );
}

function FetchError({ error, id }: { error: unknown; id: string }) {
  const kind = error instanceof IssueFetchError ? error.kind : "error";

  if (kind === "not-found") {
    return (
      <div
        role="alert"
        className="flex items-start gap-2.5 rounded-md border border-fail/40 bg-fail/5 px-3 py-3"
      >
        <TriangleAlert
          className="mt-0.5 size-3.5 shrink-0 text-fail"
          aria-hidden="true"
        />
        <div className="flex flex-col gap-0.5">
          <p className="font-mono text-sm text-foreground">{id} not found</p>
          <p className="font-sans text-xs leading-relaxed text-muted-foreground">
            Check the ticket id and that it exists in this repo's tracker.
          </p>
        </div>
      </div>
    );
  }

  if (kind === "no-tracker") {
    return (
      <div
        role="alert"
        className="flex items-start gap-2.5 rounded-md border border-warn/40 bg-warn/5 px-3 py-3"
      >
        <TriangleAlert
          className="mt-0.5 size-3.5 shrink-0 text-warn"
          aria-hidden="true"
        />
        <div className="flex flex-col gap-1">
          <p className="font-mono text-sm text-foreground">
            No direct tracker for this repo
          </p>
          <p className="font-sans text-xs leading-relaxed text-muted-foreground">
            Confirming a ticket needs direct tracker credentials. You can still
            queue by id, or add credentials in{" "}
            <Link to="/settings" className="text-primary hover:underline">
              settings
            </Link>
            .
          </p>
        </div>
      </div>
    );
  }

  return (
    <p className="font-mono text-sm text-destructive">{actionError(error)}</p>
  );
}

function EpicTag({ id }: { id: string }) {
  return (
    <span className="inline-flex shrink-0 items-center gap-1 font-mono text-[0.7rem] text-info">
      <span aria-hidden="true">◆</span>
      {id}
    </span>
  );
}

function TicketReason({ children }: { children: string }) {
  return (
    <p className="text-pretty font-mono text-[0.7rem] leading-relaxed text-muted-foreground">
      {children}
    </p>
  );
}

function SettledRow({
  repo,
  ticket,
  onPeek,
}: {
  repo: string;
  ticket: TimelineTicket;
  onPeek: (id: string) => void;
}) {
  const pill = ticketPill(ticket);
  const head = (
    <div className="flex items-center gap-3">
      <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">
        {ticket.epicId ? <EpicTag id={ticket.epicId} /> : null}
        <TicketIdButton
          id={ticket.id}
          onPeek={onPeek}
          className="text-sm text-primary"
        />
        {ticket.title ? (
          <span className="min-w-0 truncate font-sans text-sm text-foreground">
            {ticket.title}
          </span>
        ) : null}
        <InternalTag source={ticket.source} />
      </div>
      <StatusPill state={pill.state} label={pill.label} className="shrink-0" />
    </div>
  );
  const reason = ticket.reason ? (
    <TicketReason>{ticket.reason}</TicketReason>
  ) : null;

  if (ticket.hasRun) {
    return (
      <li className="border-b border-border/60 last:border-0">
        <Link
          to="/runs/$repo/$ticket"
          params={{ repo, ticket: ticket.id }}
          className="flex flex-col gap-1.5 px-4 py-2.5 transition-colors hover:bg-secondary/40"
        >
          {head}
          {reason}
        </Link>
      </li>
    );
  }
  return (
    <li className="flex flex-col gap-1.5 border-b border-border/60 px-4 py-2.5 last:border-0">
      {head}
      {reason}
    </li>
  );
}

function FinishedSection({
  repo,
  settled,
  onPeek,
}: {
  repo: string;
  settled: TimelineTicket[];
  onPeek: (id: string) => void;
}) {
  const [state, dispatch] = useReducer(finishedReducer, FINISHED_INITIAL);
  const view = finishedView(settled, state.visible);

  return (
    <section className="flex flex-col gap-2">
      <Eyebrow glyph="done">FINISHED</Eyebrow>
      <div className="overflow-hidden rounded-md border border-border">
        <button
          type="button"
          onClick={() => dispatch({ type: "toggle" })}
          aria-expanded={state.expanded}
          className={cn(
            "flex w-full items-center justify-between gap-4 px-4 py-2.5 text-left transition-colors hover:bg-secondary/40",
            state.expanded && "border-b border-border",
          )}
        >
          <span className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
            {state.expanded ? (
              <ChevronDown
                className="size-4 shrink-0 text-muted-foreground"
                aria-hidden="true"
              />
            ) : (
              <ChevronRight
                className="size-4 shrink-0 text-muted-foreground"
                aria-hidden="true"
              />
            )}
            <span className="font-mono text-sm text-foreground">
              {view.total} finished
            </span>
            <span className="font-mono text-xs text-muted-foreground">
              <span className="text-done" aria-hidden="true">
                ✓
              </span>{" "}
              {view.tally.map((t) => `${t.count} ${t.label}`).join(" · ")}
            </span>
          </span>
          {!state.expanded && view.latest ? (
            <span className="hidden shrink-0 items-center gap-2 font-mono text-xs text-muted-foreground sm:inline-flex">
              latest <span className="text-primary">{view.latest.id}</span>
            </span>
          ) : null}
        </button>

        {state.expanded ? (
          <>
            <ul className="flex flex-col">
              {view.rows.map((ticket) => (
                <SettledRow
                  key={ticket.id}
                  repo={repo}
                  ticket={ticket}
                  onPeek={onPeek}
                />
              ))}
            </ul>
            {view.older > 0 ? (
              <div className="border-t border-border px-4 py-2.5">
                <button
                  type="button"
                  onClick={() => dispatch({ type: "more" })}
                  className="font-mono text-xs text-teal underline-offset-4 hover:underline"
                >
                  Show {Math.min(view.older, FINISHED_PAGE_SIZE)} more (
                  {view.older} older)
                </button>
              </div>
            ) : null}
          </>
        ) : null}
      </div>
    </section>
  );
}

function RunningRow({
  repo,
  ticket,
  instance,
  now,
  onPeek,
}: {
  repo: string;
  ticket: TimelineTicket;
  instance?: Instance;
  now: number;
  onPeek: (id: string) => void;
}) {
  const live = instance?.ticket === ticket.id ? instance : undefined;
  const phase = live?.phase ?? ticket.phase;

  return (
    <div className="flex flex-col gap-3 rounded-md border border-teal/40 bg-teal/5 px-4 py-3">
      <div className="flex flex-wrap items-center gap-3">
        {ticket.epicId ? <EpicTag id={ticket.epicId} /> : null}
        <TicketIdButton
          id={ticket.id}
          onPeek={onPeek}
          className="text-sm text-primary"
        />
        {ticket.title ? (
          <span className="font-sans text-base text-foreground">
            {ticket.title}
          </span>
        ) : null}
        <Link
          to="/live/$repo/$ticket"
          params={{ repo, ticket: ticket.id }}
          className="inline-flex items-center gap-1.5 font-mono text-xs text-teal underline-offset-4 hover:underline"
        >
          <ExternalLink className="size-3.5" aria-hidden="true" />
          View run
        </Link>
      </div>
      {phase || live?.activity ? (
        <PhaseStepper
          {...runSteps("live", phase ?? "", live?.activity, live?.detail)}
        />
      ) : (
        <p className="font-sans text-sm text-muted-foreground">
          Picking the next ticket…
        </p>
      )}
      {live ? (
        <div className="flex flex-wrap items-center gap-6 font-mono text-xs text-muted-foreground">
          <span>
            elapsed{" "}
            <span className="text-foreground">
              {elapsedSince(live.started_at, now)}
            </span>
          </span>
          {live.state_since ? (
            <span>
              in phase{" "}
              <span className="text-foreground">
                {elapsedSince(live.state_since, now)}
              </span>
            </span>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

function PendingTicketRow({
  ticket,
  onPeek,
}: {
  ticket: TimelineTicket;
  onPeek: (id: string) => void;
}) {
  return (
    <li className="flex items-center gap-3 border-b border-border/60 px-4 py-2.5 last:border-0">
      {ticket.epicId ? <EpicTag id={ticket.epicId} /> : null}
      <TicketIdButton
        id={ticket.id}
        onPeek={onPeek}
        className="text-sm text-primary"
      />
      <span className="min-w-0 flex-1 truncate font-sans text-sm text-muted-foreground">
        {ticket.title || "—"}
      </span>
      <InternalTag source={ticket.source} />
      <ProviderTag provider={ticket.provider} />
      <StatusPill state="todo" label="pending" className="shrink-0" />
    </li>
  );
}

function PendingEpicGroup({
  entry,
  onPeek,
}: {
  entry: Extract<PendingEntry, { kind: "epic" }>;
  onPeek: (id: string) => void;
}) {
  return (
    <li className="border-b border-border/60 last:border-0">
      <div className="flex items-center gap-3 px-4 py-2.5">
        <span className="inline-flex shrink-0 items-center gap-1 font-mono text-sm text-info">
          <span aria-hidden="true">◆</span>
          <TicketIdButton id={entry.id} onPeek={onPeek} className="text-info" />
        </span>
        <span className="min-w-0 flex-1 truncate font-sans text-sm text-foreground">
          {entry.title || "—"}
        </span>
        <InternalTag source={entry.source} />
        <StatusPill
          state="info"
          label={`epic · ${entry.done}/${entry.total}`}
          className="shrink-0"
        />
      </div>
      <ul className="border-t border-border/60 bg-secondary/20">
        {entry.children.map((child) => (
          <li
            key={child.id}
            className="flex items-center gap-3 border-b border-border/40 py-1.5 pl-12 pr-4 last:border-0"
          >
            <TicketIdButton
              id={child.id}
              onPeek={onPeek}
              className="text-xs text-primary/80"
            />
            <span className="min-w-0 flex-1 truncate font-sans text-xs text-muted-foreground">
              {child.title || "—"}
            </span>
            <StatusPill state="todo" label="pending" className="shrink-0" />
          </li>
        ))}
      </ul>
    </li>
  );
}

function RunningQueueView({
  repo,
  queue,
  timeline,
  instance,
  takeover,
  halt,
  onStop,
  stopping,
  stopError,
  hasRunningChild,
  shuttingDown,
  onShutdown,
  shutdownError,
  onPeek,
}: {
  repo: string;
  queue: QueueResponse;
  timeline: Timeline;
  instance?: Instance;
  takeover?: Instance;
  halt: LoopHalt | null;
  onStop: () => void;
  stopping: boolean;
  stopError: unknown;
  hasRunningChild: boolean;
  shuttingDown: boolean;
  onShutdown: () => void;
  shutdownError: unknown;
  onPeek: (id: string) => void;
}) {
  const now = useNow(1000);
  const queryClient = useQueryClient();
  const [addOpen, setAddOpen] = useState(false);

  return (
    <div className="flex flex-col gap-6">
      <LoopBanner repo={repo} takeover={takeover} halt={halt} />

      <TerminalCard title="loop" className="max-w-3xl">
        <div className="flex flex-col gap-6">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <span className="font-mono text-sm text-muted-foreground">
              <span className="text-foreground">
                {timeline.done}/{timeline.total}
              </span>{" "}
              tickets done
            </span>
            <div className="flex items-center gap-4">
              {timeline.elapsedAnchor ? (
                <span className="font-mono text-xs text-muted-foreground">
                  elapsed{" "}
                  <span className="text-foreground">
                    {elapsedSince(timeline.elapsedAnchor, now)}
                  </span>
                </span>
              ) : null}
              <StatusPill
                state="active"
                label={
                  timeline.running
                    ? stepName(
                        timeline.running.activity,
                        timeline.running.phase ?? "",
                      ).toLowerCase() || "draining"
                    : "draining"
                }
              />
            </div>
          </div>

          <section className="flex flex-col gap-2">
            <Eyebrow glyph="active">RUNNING</Eyebrow>
            {timeline.running ? (
              <RunningRow
                repo={repo}
                ticket={timeline.running}
                instance={instance}
                now={now}
                onPeek={onPeek}
              />
            ) : (
              <p className="font-sans text-sm text-muted-foreground">
                Idle — picking the next ticket from the queue.
              </p>
            )}
          </section>

          <section className="flex flex-col gap-2">
            <div className="flex items-center justify-between gap-3">
              <Eyebrow glyph="idle">REMAINING</Eyebrow>
              <button
                type="button"
                onClick={() => setAddOpen(true)}
                disabled={shuttingDown}
                className="inline-flex items-center gap-1.5 font-mono text-xs text-teal underline-offset-4 hover:underline disabled:pointer-events-none disabled:opacity-30"
              >
                <Plus className="size-3.5" aria-hidden="true" />
                Add ticket
              </button>
            </div>
            {timeline.pending.length > 0 ? (
              <>
                <div className="overflow-hidden rounded-md border border-border">
                  <ul className="flex flex-col">
                    {timeline.pending.map((entry) =>
                      entry.kind === "epic" ? (
                        <PendingEpicGroup
                          key={entry.id}
                          entry={entry}
                          onPeek={onPeek}
                        />
                      ) : (
                        <PendingTicketRow
                          key={entry.ticket.id}
                          ticket={entry.ticket}
                          onPeek={onPeek}
                        />
                      ),
                    )}
                  </ul>
                </div>
                <p className="font-sans text-xs leading-relaxed text-muted-foreground">
                  Remaining tickets — the pick order is decided at run time, not
                  promised here.
                </p>
              </>
            ) : (
              <EmptyState
                message="Nothing left in the queue — add a ticket and the drain picks it up."
                actions={
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    className="font-mono"
                    onClick={() => setAddOpen(true)}
                    disabled={shuttingDown}
                  >
                    <Plus className="size-4" aria-hidden="true" />
                    Add ticket
                  </Button>
                }
              />
            )}
          </section>

          {timeline.settled.length > 0 ? (
            <FinishedSection
              repo={repo}
              settled={timeline.settled}
              onPeek={onPeek}
            />
          ) : null}
        </div>
      </TerminalCard>

      <div className="flex max-w-3xl flex-wrap items-end justify-end gap-4">
        <div className="flex flex-col items-end gap-2">
          {stopError ? (
            <p className="font-mono text-xs text-destructive">
              {actionError(stopError)}
            </p>
          ) : null}
          <ConfirmDialog
            windowTitle="confirm"
            trigger={
              <Button
                variant="destructive"
                size="sm"
                className="font-mono"
                disabled={stopping || shuttingDown}
              >
                <Square className="size-4" aria-hidden="true" />
                {stopping ? "Stopping…" : "Stop queue"}
              </Button>
            }
            title={`Stop the queue on ${repo}?`}
            description="The run stops now. Work in progress is saved at the last checkpoint and the ticket stays resumable — Start again to pick it up from there."
            confirmLabel="Stop queue"
            destructive
            onConfirm={onStop}
          />
        </div>
        <ShutdownAction
          repo={repo}
          queuedCount={timeline.pending.length}
          hasRunningChild={hasRunningChild}
          shuttingDown={shuttingDown}
          onConfirm={onShutdown}
          error={shutdownError}
        />
      </div>

      <AddTicketDialog
        repo={repo}
        queued={queue.items}
        open={addOpen}
        onOpenChange={setAddOpen}
        onQueue={(res) => publishQueue(queryClient, repo, res)}
      />
    </div>
  );
}

interface HaltNotice {
  tone: "info" | "warn" | "fail";
  glyph: string;
  headline: string;
  hint: string;
}

function haltNotice(halt: LoopHalt): HaltNotice {
  const ticket = halt.ticket || "the ticket";
  switch (halt.kind) {
    case "stopped":
      return {
        tone: "info",
        glyph: "⏹",
        headline: STOPPED_HEADLINE,
        hint: STOPPED_HINT,
      };
    case "paused":
      return pauseKind(halt.reason) === "reauth"
        ? {
            tone: "warn",
            glyph: "⚠",
            headline: "paused — re-authentication needed",
            hint: "This is not a failure. Re-login to the provider, then the queue resumes.",
          }
        : {
            tone: "warn",
            glyph: "⚠",
            headline: "paused — rate limit reached",
            hint: "This is not a failure. The queue resumes on its own once the provider's usage window clears.",
          };
    case "budget":
      return {
        tone: "warn",
        glyph: "⚠",
        headline: "budget stop",
        hint: `${halt.reason || "The budget cap was reached"}. The queue stops for the day — raise BUDGET in Settings to keep going.`,
      };
    case "fault":
      return {
        tone: "fail",
        glyph: "✗",
        headline: "fault",
        hint: `${ticket} left the pipeline in an unexpected state. Work in progress is preserved — open the run to intervene.`,
      };
    default:
      return {
        tone: "fail",
        glyph: "✗",
        headline: "quarantined",
        hint: `${ticket} needs a human — open the run to see why.`,
      };
  }
}

function TakeoverBanner({ repo, ticket }: { repo: string; ticket?: string }) {
  return (
    <div className="flex max-w-3xl items-start gap-2.5 rounded-lg border border-info/40 bg-info/10 px-4 py-3">
      <SquareTerminal
        className="mt-0.5 size-4 shrink-0 text-info"
        aria-hidden="true"
      />
      <div className="flex flex-col gap-1">
        <span className="font-mono text-sm text-info">Taken over</span>
        <p className="font-sans text-sm leading-relaxed text-foreground">
          {ticket ? `${ticket} is` : "This repo is"} taken over in a terminal —
          close it, then use Run next to hand the ticket back.
        </p>
        {ticket ? (
          <Link
            to="/live/$repo/$ticket"
            params={{ repo, ticket }}
            className="mt-1 inline-flex w-fit items-center gap-1.5 font-mono text-xs text-teal underline-offset-4 hover:underline"
          >
            <ExternalLink className="size-3.5" aria-hidden="true" />
            Open {ticket}
          </Link>
        ) : null}
      </div>
    </div>
  );
}

const HALT_TONE: Record<
  HaltNotice["tone"],
  { border: string; bg: string; text: string }
> = {
  info: { border: "border-info/40", bg: "bg-info/10", text: "text-info" },
  warn: { border: "border-warn/40", bg: "bg-warn/10", text: "text-warn" },
  fail: { border: "border-fail/40", bg: "bg-fail/10", text: "text-fail" },
};

// LoopBanner is the page's single headline slot: a live takeover owns it, and a
// halt stored behind one is history rather than a second, contradicting banner.
function LoopBanner({
  repo,
  takeover,
  halt,
}: {
  repo: string;
  takeover?: Instance;
  halt: LoopHalt | null;
}) {
  if (takeover) return <TakeoverBanner repo={repo} ticket={takeover.ticket} />;
  if (halt) return <HaltBanner repo={repo} halt={halt} />;
  return null;
}

function HaltBanner({ repo, halt }: { repo: string; halt: LoopHalt }) {
  const notice = haltNotice(halt);
  const { border, bg, text: glyphColor } = HALT_TONE[notice.tone];
  return (
    <div
      className={cn(
        "flex max-w-3xl items-start gap-2.5 rounded-lg border px-4 py-3",
        border,
        bg,
      )}
    >
      <span
        className={cn("mt-0.5 shrink-0 font-mono text-sm", glyphColor)}
        aria-hidden="true"
      >
        {notice.glyph}
      </span>
      <div className="flex flex-col gap-1">
        <span className={cn("font-mono text-sm", glyphColor)}>
          {notice.headline}
        </span>
        <p className="font-sans text-sm leading-relaxed text-foreground">
          {notice.hint}
        </p>
        {halt.ticket ? (
          <Link
            to="/live/$repo/$ticket"
            params={{ repo, ticket: halt.ticket }}
            className="mt-1 inline-flex w-fit items-center gap-1.5 font-mono text-xs text-teal underline-offset-4 hover:underline"
          >
            <ExternalLink className="size-3.5" aria-hidden="true" />
            Open {halt.ticket}
          </Link>
        ) : null}
      </div>
    </div>
  );
}

// loopTitleState reads the loop's tab-title state from the same signals the card
// renders: the halt banner, the running header's done/total and step pill, or a
// clean drain. It never re-derives a state the page does not already show.
function loopTitleState(
  canRun: boolean,
  halt: LoopHalt | null,
  view: LoopView,
  timeline: Timeline | null,
): LoopTitleState {
  if (!canRun) return { kind: "idle" };
  if (halt) return { kind: "halted", halt: halt.kind, ticket: halt.ticket };
  if (view === "running" && timeline) {
    const running = timeline.running;
    const step = running
      ? stepName(running.activity, running.phase ?? "").toLowerCase() ||
        "draining"
      : "draining";
    return {
      kind: "draining",
      done: timeline.done,
      total: timeline.total,
      ticket: running?.id ?? "",
      step,
    };
  }
  if (timeline && timeline.total > 0 && timeline.done === timeline.total) {
    return { kind: "done", total: timeline.total };
  }
  return { kind: "idle" };
}

export function Loop() {
  const queryClient = useQueryClient();
  const { repo: activeRepo, repos } = useActiveRepo();
  const repo = activeRepo ?? "";

  const startable = repos.filter((r) => r.allowed).map((r) => r.name);
  const canRun = repo !== "" && startable.includes(repo);

  // shutdownArmed bridges the gap between confirming Shut down and the queue
  // query catching up: it forces polling on immediately instead of waiting for
  // a fetch that happens to land after the click to prove shutting_down is
  // true, and it keeps polling alive until one lands showing teardown is done.
  const [shutdownArmed, setShutdownArmed] = useState(false);

  const queue = useQuery({
    ...queueQueryOptions(repo),
    refetchInterval: (q) =>
      q.state.data?.draining || q.state.data?.shutting_down || shutdownArmed
        ? 3000
        : false,
  });
  const { data: instData } = useQuery(instancesQueryOptions);
  const liveInstance = repoInstance(instData?.instances ?? [], repo);
  const takeoverInstance = isTakeover(liveInstance) ? liveInstance : undefined;
  const runs = useQuery(runsQueryOptions(repo));

  // The peeked issue lives in the URL, so queue polling never closes the drawer
  // and /loop?issue=COD-123 deep-links straight into the preview.
  const [peek, setPeek] = useQueryState(
    "issue",
    parseAsString.withOptions({ history: "push" }),
  );
  const onPeek = (id: string) => void setPeek(id);

  const { view, timeline, halt } = projectLoopState({
    queue: queue.data,
    runs: runs.data?.runs ?? [],
    instance: liveInstance,
  });
  usePageTitle(loopTitle(loopTitleState(canRun, halt, view, timeline)));

  const stop = useMutation({
    mutationFn: () => drain(repo, false),
    onSuccess: (res) => publishQueue(queryClient, repo, res),
  });

  const shutdown = useMutation({
    mutationFn: () => shutdownQueue(repo),
    onMutate: () => setShutdownArmed(true),
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: queueQueryOptions(repo).queryKey }),
  });

  const shuttingDown =
    shutdownArmed || (queue.data?.shutting_down ?? false) || shutdown.isPending;

  useEffect(() => {
    if (shutdownArmed && queue.data && !queue.data.shutting_down && !queue.data.draining) {
      setShutdownArmed(false);
    }
  }, [shutdownArmed, queue.data]);

  useEffect(() => {
    stop.reset();
    shutdown.reset();
    setShutdownArmed(false);
  }, [repo]);

  if (!canRun) {
    return (
      <NotStartableNotice
        repo={repo}
        root={repos.find((r) => r.name === repo)?.root}
      />
    );
  }

  const drawer = (
    <IssueDrawer
      repo={repo}
      issueId={peek}
      onOpenChange={(open) => {
        if (!open) void setPeek(null);
      }}
      onSelectIssue={onPeek}
    />
  );

  if (view === "running" && queue.data && timeline) {
    return (
      <>
        <RunningQueueView
          repo={repo}
          queue={queue.data}
          timeline={timeline}
          instance={liveInstance}
          takeover={takeoverInstance}
          halt={halt}
          onStop={() => stop.mutate()}
          stopping={stop.isPending}
          stopError={stop.error}
          hasRunningChild={Boolean(liveInstance)}
          shuttingDown={shuttingDown}
          onShutdown={() => shutdown.mutate()}
          shutdownError={shutdown.error}
          onPeek={onPeek}
        />
        {drawer}
      </>
    );
  }

  return (
    <div className="flex flex-col gap-6">
      <LoopBanner repo={repo} takeover={takeoverInstance} halt={halt} />
      <LaunchQueueCard
        repo={repo}
        freshness={repos.find((r) => r.name === repo)?.freshness}
        hasRunningChild={Boolean(liveInstance)}
        shuttingDown={shuttingDown}
        onShutdown={() => shutdown.mutate()}
        shutdownError={shutdown.error}
        onPeek={onPeek}
      />
      {drawer}
    </div>
  );
}

function NotStartableNotice({ repo, root }: { repo: string; root?: string }) {
  return (
    <TerminalCard title="loop" className="max-w-3xl">
      <div className="flex flex-col items-start gap-4">
        <p className="font-sans text-sm leading-relaxed text-muted-foreground">
          {repo
            ? `${repo} is observe-only — the hub can browse its runs but isn't cleared to start loops here yet.`
            : "No repo checked out yet. Register a repo to start a loop."}
        </p>
        <div className="flex flex-wrap items-center gap-2">
          {root && (
            <MakeStartableButton
              root={root}
              name={repo}
              className="font-mono"
            />
          )}
          <Button asChild variant="outline" size="sm" className="font-mono">
            <Link to="/instances">Manage repos</Link>
          </Button>
        </div>
      </div>
    </TerminalCard>
  );
}
