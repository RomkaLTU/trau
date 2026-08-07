import { useEffect, useReducer, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import {
  ArrowDown,
  ArrowRight,
  ArrowUp,
  Check,
  ChevronDown,
  ChevronRight,
  ChevronsUp,
  ExternalLink,
  Info,
  Layers,
  ListPlus,
  Play,
  Plus,
  RefreshCw,
  Search,
  Square,
  SquareTerminal,
  TriangleAlert,
  Wrench,
  X,
} from "lucide-react";
import { parseAsString, useQueryState } from "nuqs";

import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { IssueDrawer } from "@/components/issue-drawer";
import { MakeStartableButton } from "@/components/make-startable-button";
import { useProposeFix } from "@/components/grill/propose-fix";
import { useActiveRepo } from "@/components/trau/active-repo";
import { AddTicketDialog } from "@/components/trau/add-ticket-dialog";
import { BabysitterCard } from "@/components/trau/babysitter-card";
import { MemberRepoField } from "@/components/trau/member-repo-picker";
import { RepoPicker } from "@/components/trau/repo-picker";
import { TargetRepoField } from "@/components/trau/target-repo-field";
import { useStartRepo } from "@/lib/start-repo";
import { ConfirmDialog } from "@/components/trau/confirm-dialog";
import { EmptyState } from "@/components/trau/empty-state";
import { Eyebrow } from "@/components/trau/eyebrow";
import { useHandback } from "@/components/trau/handback-dialog";
import { RunOptions, useRunSteps } from "@/components/trau/run-steps-dialog";
import { PhaseStepper } from "@/components/trau/phase-stepper";
import { PRStatusBadge } from "@/components/trau/pr-status-badge";
import type { PaneTab } from "@/components/trau/run-view";
import { SegmentedControl } from "@/components/trau/segmented-control";
import { StatusPill, type RunState } from "@/components/trau/status-pill";
import { SyncStateLine } from "@/components/trau/sync-state";
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
import { syncedAgo, useNow } from "@/lib/elapsed";
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
  batchDisplayName,
  batchMembers,
  batchName,
  batchSelectable,
  batchStartBlocker,
  batchSummary,
  createBatch,
  dequeue,
  dismissBatch,
  drain,
  enqueueFresh,
  moveQueueItem,
  promoteQueueItem,
  publishQueue,
  queueExecutable,
  queueLive,
  queueQueryOptions,
  queueRunnable,
  QUEUE_NOT_RUNNABLE,
  releaseGateLabel,
  requeueIssue,
  runNext as runNextRequest,
  runQueueItem,
  skipResumeApplies,
  spawnHoldReason,
  startBatch,
  stopQueue,
  updateBatch,
  type BatchSummary,
  type OnFault,
  type QueueBatch,
  type QueueItem,
  type QueueResponse,
} from "@/lib/queue";
import {
  removeFromQueueLabel,
  removeFromQueueTitle,
  removeFromQueueWarning,
} from "@/lib/queue-remove";
import {
  pauseKind,
  runSteps,
  STOPPED_HEADLINE,
  STOPPED_HINT,
} from "@/lib/runlive";
import { canonicalSkips, skipLabel } from "@/lib/skips";
import { isAwaitingHuman, stepName, syncState, withSkips } from "@/lib/steps";
import { runsQueryOptions, type PRStatus } from "@/lib/runs";
import {
  builderView,
  finalizePill,
  finishedReducer,
  finishedView,
  ticketPill,
  FINISHED_INITIAL,
  FINISHED_PAGE_SIZE,
  type FinalizeEntry,
  type PendingEntry,
  type Timeline,
  type TimelineTicket,
} from "@/lib/timeline";

const NO_OVERRIDE = "default";

function actionError(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function ActionCaption({ children }: { children: string }) {
  return (
    <p className="text-pretty text-right font-mono text-[0.65rem] leading-relaxed text-muted-foreground">
      {children}
    </p>
  );
}

// RemoveFromQueueDialog confirms taking one item out of the queue and spells out
// that the ticket survives, so the gesture reads nothing like the ticket Delete
// a user reaches for when this one refuses. A running item is stopped first.
function RemoveFromQueueDialog({
  item,
  onOpenChange,
  onConfirm,
}: {
  item: QueueItem | undefined;
  onOpenChange: (open: boolean) => void;
  onConfirm: (item: QueueItem) => void;
}) {
  if (!item) return null;

  return (
    <ConfirmDialog
      open
      onOpenChange={onOpenChange}
      windowTitle="remove from queue"
      title={removeFromQueueTitle(item)}
      description={removeFromQueueWarning(item)}
      confirmLabel={removeFromQueueLabel(item)}
      destructive
      onConfirm={() => onConfirm(item)}
    />
  );
}

// RemoveFromQueueButton is the queue's own X: it ejects the row's work
// altogether — the saved progress goes and the ticket returns to Ready. It stays
// enabled on a running item — the confirm behind it stops the run first — and
// reads as waiting while that stop is in flight.
function RemoveFromQueueButton({
  item,
  disabled,
  onRemove,
}: {
  item: QueueItem;
  disabled: boolean;
  onRemove: (id: string) => void;
}) {
  const removing = item.removing ?? false;

  return (
    <button
      type="button"
      onClick={() => onRemove(item.id)}
      disabled={disabled || removing}
      title={
        removing ? "Removing…" : "Remove from queue (the ticket goes back to Ready)"
      }
      aria-label={`Remove ${item.id} from queue`}
      className="flex size-6 shrink-0 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-secondary hover:text-fail disabled:pointer-events-none disabled:opacity-30"
    >
      {removing ? (
        <RefreshCw className="size-3.5 animate-spin" aria-hidden="true" />
      ) : (
        <X className="size-3.5" aria-hidden="true" />
      )}
    </button>
  );
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
      synced {syncedAgo(freshness.last_synced_at, now)}
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
  "awaiting-merge": "warn",
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
  quarantined: { glyph: "✕", className: "text-fail", label: "quarantined" },
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

// BacklogPRBadge shows awaiting-merge only: a merged ticket leaves the backlog
// for Done and a closed one already carries its quarantine pill.
function BacklogPRBadge({ status }: { status?: PRStatus }) {
  if (status !== "awaiting-merge") return null;
  return <PRStatusBadge status={status} className="shrink-0" />;
}

// ProviderTag names the provider a queued run will use when it is not the configured
// default: the item's own one-shot override, else the provider pinned on the issue.
function ProviderTag({ provider, pin }: { provider?: string; pin?: string }) {
  const name = provider || pin;
  if (!name) return null;
  return (
    <span
      title={provider ? "provider · this run only" : "provider · pinned on issue"}
      className="shrink-0 rounded-sm border border-border bg-secondary/60 px-1.5 py-0.5 font-mono text-[0.6rem] text-muted-foreground"
    >
      {name}
    </span>
  );
}

function SkipsTag({ skips }: { skips?: string[] }) {
  const label = skipLabel(skips);
  if (!label) return null;
  return (
    <span
      title="pipeline work this item's run bypasses"
      className="shrink-0 rounded-sm border border-warn/40 bg-warn/10 px-1.5 py-0.5 font-mono text-[0.6rem] text-warn"
    >
      {label}
    </span>
  );
}

function BatchChip({ name }: { name: string }) {
  return (
    <span
      title="batch"
      className="inline-flex shrink-0 items-center gap-1 rounded-sm border border-teal/40 bg-teal/10 px-1.5 py-0.5 font-mono text-[0.65rem] text-teal"
    >
      <Layers className="size-3" aria-hidden="true" />
      {name}
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
  batch,
  selected,
  onSelect,
  onToggle,
  onMove,
  onRun,
  onRemove,
  onPeek,
}: {
  item: QueueItem;
  index: number;
  count: number;
  expanded: boolean;
  busy: boolean;
  batch: string;
  selected: boolean;
  onSelect: () => void;
  onToggle: () => void;
  onMove: (dir: -1 | 1) => void;
  onRun: () => void;
  onRemove: (id: string) => void;
  onPeek: (id: string) => void;
}) {
  const isEpic = item.kind === "epic";
  const { done, total } = epicCounts(item);
  const subs = item.sub_issues ?? [];
  const runnable = item.status === "pending" || item.status === "paused";
  const runHint = item.blocked
    ? `Blocked by ${(item.blockers ?? []).join(", ")}`
    : isEpic
      ? "Run remaining sub-issues, then stop"
      : "Run only this item";

  return (
    <li className="border-b border-border/60 last:border-0">
      <div className="flex items-center gap-3 px-3 py-2.5">
        {batchSelectable(item) ? (
          <button
            type="button"
            role="checkbox"
            aria-checked={selected}
            aria-label={`Select ${item.id} for a batch`}
            onClick={onSelect}
            className={cn(
              "flex size-4 shrink-0 items-center justify-center rounded-sm border transition-colors",
              selected
                ? "border-primary bg-primary text-primary-foreground"
                : "border-border bg-input hover:border-primary/60",
            )}
          >
            {selected ? <Check className="size-3" aria-hidden="true" /> : null}
          </button>
        ) : (
          <span className="size-4 shrink-0" aria-hidden="true" />
        )}

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
        <ProviderTag provider={item.provider} pin={item.provider_pin} />
        <SkipsTag skips={item.skips} />
        {batch ? <BatchChip name={batch} /> : null}

        {isEpic ? (
          <StatusPill state="info" label={`epic · ${done}/${total}`} />
        ) : item.status !== "pending" ? (
          <StatusPill state={statusState(item.status)} label={item.status} />
        ) : (
          <StatusPill state="todo" label="ticket" />
        )}

        <div className="flex shrink-0 items-center gap-0.5">
          {runnable && (
            <span title={runHint} className="flex">
              <button
                type="button"
                onClick={onRun}
                disabled={item.blocked || busy}
                aria-label={`Run ${item.id} now`}
                className="flex size-6 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-secondary hover:text-primary disabled:pointer-events-none disabled:opacity-30"
              >
                <Play className="size-3.5" aria-hidden="true" />
              </button>
            </span>
          )}
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
          <RemoveFromQueueButton
            item={item}
            disabled={busy}
            onRemove={onRemove}
          />
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

// NewBatchDialog files the picked rows under one batch. The name is optional —
// an unnamed batch is shown by the date it was filed instead.
function NewBatchDialog({
  count,
  pending,
  error,
  onOpenChange,
  onCreate,
}: {
  count: number;
  pending: boolean;
  error: unknown;
  onOpenChange: (open: boolean) => void;
  onCreate: (name: string) => void;
}) {
  const [name, setName] = useState("");

  return (
    <AlertDialog open onOpenChange={onOpenChange}>
      <AlertDialogContent
        aria-describedby={undefined}
        className="gap-0 overflow-hidden border-border bg-popover p-0 shadow-xl sm:max-w-sm"
      >
        <div className="flex items-center gap-3 border-b border-border px-4 py-2.5">
          <div className="flex items-center gap-1.5" aria-hidden="true">
            <span className="size-2.5 rounded-full bg-fail" />
            <span className="size-2.5 rounded-full bg-warn" />
            <span className="size-2.5 rounded-full bg-done" />
          </div>
          <AlertDialogTitle asChild>
            <span className="font-mono text-xs font-normal text-muted-foreground">
              new-batch
            </span>
          </AlertDialogTitle>
        </div>
        <form
          className="flex flex-col gap-3 px-4 py-4"
          onSubmit={(e) => {
            e.preventDefault();
            onCreate(name.trim());
          }}
        >
          <label
            htmlFor="batch-name"
            className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground"
          >
            name · optional
          </label>
          <Input
            id="batch-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="API polish"
            autoComplete="off"
            spellCheck={false}
            className="h-auto px-2.5 py-1.5 font-mono text-sm placeholder:text-muted-foreground/60"
          />
          <p className="font-sans text-xs leading-relaxed text-muted-foreground">
            {count} {count === 1 ? "item joins" : "items join"} the batch. Start
            batch runs its members in queue order, then the loop stops — the rest
            of the queue stays where it is.
          </p>
          {error ? (
            <p className="font-mono text-xs text-fail" role="alert">
              {actionError(error)}
            </p>
          ) : null}
          <div className="flex justify-end gap-2">
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="font-mono"
              onClick={() => onOpenChange(false)}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              size="sm"
              className="font-mono"
              disabled={pending}
            >
              {pending ? "Filing…" : "Create batch"}
            </Button>
          </div>
        </form>
      </AlertDialogContent>
    </AlertDialog>
  );
}

function batchCounts(summary: BatchSummary): string {
  const held = `${summary.members} ${summary.members === 1 ? "item" : "items"}`;
  const outcomes = summary.tally.map((t) => `${t.count} ${t.status}`);
  return [held, ...outcomes].join(" · ");
}

// BatchMemberRow is view-only: the id opens the drawer and nothing else on the
// row acts, so the expanded card reads the batch without editing it.
function BatchMemberRow({
  item,
  onPeek,
}: {
  item: QueueItem;
  onPeek: (id: string) => void;
}) {
  const { done, total } = epicCounts(item);

  return (
    <li className="flex items-center gap-3 border-b border-border/40 py-1.5 pl-9 pr-3 last:border-0">
      <TicketIdButton
        id={item.id}
        onPeek={onPeek}
        className="text-xs text-primary/80"
      />
      <span className="min-w-0 flex-1 truncate font-sans text-xs text-muted-foreground">
        {item.title || "—"}
      </span>
      {item.blocked ? (
        <span
          title={`Blocked by ${(item.blockers ?? []).join(", ")}`}
          className="shrink-0 rounded-sm border border-warn/40 bg-warn/10 px-1.5 py-0.5 font-mono text-[0.6rem] uppercase tracking-[0.14em] text-warn"
        >
          blocked
        </span>
      ) : null}
      {item.kind === "epic" ? (
        <span className="shrink-0 font-mono text-[0.65rem] text-muted-foreground">
          epic · {done}/{total}
        </span>
      ) : null}
      <StatusPill state={statusState(item.status)} label={item.status} />
    </li>
  );
}

// BatchCard's Start is a scoped Start — it runs the batch's members and stops at
// the batch boundary — and says why whenever it cannot.
function BatchCard({
  batch,
  summary,
  members,
  expanded,
  blocker,
  busy,
  starting,
  error,
  onToggle,
  onStart,
  onRename,
  onDismiss,
  onPeek,
}: {
  batch: QueueBatch;
  summary: BatchSummary;
  members: QueueItem[];
  expanded: boolean;
  blocker: string;
  busy: boolean;
  starting: boolean;
  error: unknown;
  onToggle: () => void;
  onStart: () => void;
  onRename: (name: string) => void;
  onDismiss: () => void;
  onPeek: (id: string) => void;
}) {
  const [draft, setDraft] = useState<string | null>(null);
  const label = batchDisplayName(batch);
  const save = (name: string) => {
    onRename(name.trim());
    setDraft(null);
  };

  return (
    <div className="flex flex-col gap-2 overflow-hidden rounded-md border border-border bg-secondary/20 px-3 py-2.5">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <button
          type="button"
          onClick={onToggle}
          aria-expanded={expanded}
          aria-label={expanded ? `Collapse ${label}` : `Expand ${label}`}
          className="shrink-0 text-muted-foreground transition-colors hover:text-foreground"
        >
          {expanded ? (
            <ChevronDown className="size-3.5" aria-hidden="true" />
          ) : (
            <ChevronRight className="size-3.5" aria-hidden="true" />
          )}
        </button>

        {draft === null ? (
          <>
            <span className="inline-flex items-center gap-1.5 font-mono text-sm text-foreground">
              <Layers className="size-3.5 text-teal" aria-hidden="true" />
              {label}
            </span>
            <span className="font-mono text-xs text-muted-foreground">
              · {batchCounts(summary)}
            </span>
          </>
        ) : (
          <div className="flex flex-1 flex-wrap items-center gap-2">
            <Input
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Escape") setDraft(null);
                if (e.key === "Enter" && !e.nativeEvent.isComposing) {
                  e.preventDefault();
                  save(draft);
                }
              }}
              aria-label={`Rename ${label}`}
              placeholder="API polish"
              autoFocus
              autoComplete="off"
              spellCheck={false}
              className="h-auto w-48 px-2.5 py-1 font-mono text-sm placeholder:text-muted-foreground/60"
            />
            <Button
              type="button"
              size="sm"
              className="font-mono"
              onClick={() => save(draft)}
            >
              Save
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="font-mono"
              onClick={() => setDraft(null)}
            >
              Cancel
            </Button>
          </div>
        )}

        <div className="ml-auto flex shrink-0 items-center gap-1.5">
          <Button
            type="button"
            size="sm"
            className="font-mono"
            onClick={onStart}
            disabled={blocker !== "" || busy}
            title={blocker || "Run this batch's members, then stop"}
          >
            <Play className="size-3.5" aria-hidden="true" />
            {starting ? "Starting…" : "Start batch"}
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="font-mono"
            onClick={() => setDraft(batch.name)}
            disabled={draft !== null}
          >
            Rename
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="font-mono"
            onClick={onDismiss}
          >
            Dismiss
          </Button>
        </div>
      </div>
      {error ? (
        <p className="font-mono text-xs text-fail" role="alert">
          {actionError(error)}
        </p>
      ) : blocker ? (
        <p className="font-mono text-xs text-muted-foreground">{blocker}</p>
      ) : null}

      {expanded &&
        (members.length > 0 ? (
          <ul className="-mx-3 -mb-2.5 border-t border-border/60 bg-secondary/20">
            {members.map((m) => (
              <BatchMemberRow key={m.id} item={m} onPeek={onPeek} />
            ))}
          </ul>
        ) : (
          <p className="-mx-3 -mb-2.5 border-t border-border/60 bg-secondary/20 py-1.5 pl-9 pr-3 font-mono text-xs text-muted-foreground">
            No items
          </p>
        ))}
    </div>
  );
}

function LaunchQueueCard({
  repo,
  freshness,
  onPeek,
}: {
  repo: string;
  freshness?: RepoFreshness;
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
  const [addSkips, setAddSkips] = useState<string[]>([]);
  const [addSkipsOpen, setAddSkipsOpen] = useState(false);
  const [expandedIds, setExpandedIds] = useState<string[]>([]);
  const [browseOpen, setBrowseOpen] = useState(false);
  const [removeId, setRemoveId] = useState<string | null>(null);
  const [skipResume, setSkipResume] = useState(false);
  const [onFault, setOnFault] = useState<OnFault>("halt");
  const [selected, setSelected] = useState<string[]>([]);
  const [batchOpen, setBatchOpen] = useState(false);
  const [dismissId, setDismissId] = useState<string | null>(null);
  const [expandedBatchIds, setExpandedBatchIds] = useState<string[]>([]);

  const batches = queue.data?.batches ?? [];
  // Selection is held by id and read back through what is still selectable, so a
  // row that gets batched or settles drops out of the pick on its own.
  const selectable = new Set(items.filter(batchSelectable).map((it) => it.id));
  const picked = selected.filter((id) => selectable.has(id));

  const config = useQuery(configQueryOptions(repo));
  const providers = [NO_OVERRIDE, ...(config.data?.providers ?? [])];
  const issue = useQuery(issueQueryOptions(repo, submittedId));
  const ticket = issue.data;
  const addState = addByIdState(submittedId, ticket, issue.error);
  const warning =
    ticket && !addState.wrongProject ? statusWarning(ticket) : null;
  const overrideProvider = provider === NO_OVERRIDE ? undefined : provider;

  const startRepo = useStartRepo(repo, submittedId, submittedId !== "");

  const setQueue = (res: QueueResponse) => publishQueue(queryClient, repo, res);
  const setStartQueue = (res: QueueResponse) =>
    publishQueue(queryClient, startRepo.target, res);

  const resetAdd = () => {
    setDraft("");
    setSubmittedId("");
    setProvider(NO_OVERRIDE);
    setAddSkips([]);
    setAddSkipsOpen(false);
  };

  useEffect(() => {
    resetAdd();
    setSelected([]);
  }, [repo]);

  // A ticket the queue already holds opens on the set it carries — expanded, so
  // the narrower run is visible — and re-adding it never silently widens the run
  // back out. The seed waits for the queue to answer, else a stored set reads as
  // empty; it lands once per id, so neither a later poll nor a re-render can
  // clobber a choice made here.
  const seededId = useRef<string | null>(null);
  useEffect(() => {
    if (seededId.current === submittedId) return;
    if (submittedId !== "" && !queue.data) return;
    const stored = canonicalSkips(
      items.find((it) => it.id === submittedId)?.skips ?? [],
    );
    seededId.current = submittedId;
    setAddSkips(stored);
    setAddSkipsOpen(stored.length > 0);
  }, [submittedId, queue.data]);

  const add = useMutation({
    mutationFn: () =>
      enqueueFresh(startRepo.target, {
        id: submittedId,
        provider: overrideProvider,
        skips: addSkips,
      }),
    onSuccess: (res) => {
      setStartQueue(res);
      resetAdd();
    },
  });

  // Run next is one gesture: land the ticket in the first pending slot, then arm
  // the drain. Landing is this page's timeline — the queue response flips the
  // view to running, never a live-page navigation. The skip set is the launch's
  // own variable, so nothing but the picker that confirmed it can decide what
  // this run bypasses.
  const runNext = useMutation({
    mutationFn: (skips: string[]) =>
      runNextRequest(
        startRepo.target,
        { id: submittedId, provider: overrideProvider, skips },
        { no_resume: skipResume && skipResumeShown, on_fault: onFault },
      ),
    onSuccess: (res) => {
      setStartQueue(res);
      resetAdd();
    },
  });

  const addAll = useMutation({
    mutationFn: async () => {
      const errors: string[] = [];
      for (const it of addAllPlan.items) {
        try {
          setQueue(await enqueueFresh(repo, { id: it.id, kind: it.kind }));
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
    mutationFn: (item: QueueItem) =>
      dequeue(repo, item.id, { stop: item.status === "running" }),
    onSuccess: setQueue,
  });
  const askRemove = (id: string) => {
    remove.reset();
    setRemoveId(id);
  };
  const itemsById = new Map(items.map((it) => [it.id, it]));
  const removeTarget = removeId ? itemsById.get(removeId) : undefined;

  // Run next chains its two confirmations in the order the run reads them: the
  // hand-back settles what the last run left behind, then the step picker says
  // what this one does, and its answer is what launches.
  const runSteps = useRunSteps((_target, skips) => runNext.mutate(skips));
  const handback = useHandback(repo, (ticket) =>
    runSteps.request({
      repo: startRepo.target,
      id: ticket,
      skips: addSkips,
      confirmLabel: "Run next",
      note: runNextCopy(ticket, pendingBehind(items, ticket)),
    }),
  );

  const runOne = useMutation({
    mutationFn: (id: string) => runQueueItem(repo, id),
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

  const newBatch = useMutation({
    mutationFn: (name: string) => createBatch(repo, picked, name || undefined),
    onSuccess: (res) => {
      setQueue(res);
      setSelected([]);
      setBatchOpen(false);
    },
  });

  const launchBatch = useMutation({
    mutationFn: (id: string) =>
      startBatch(repo, id, {
        no_resume: skipResume && skipResumeShown,
        on_fault: onFault,
      }),
    onSuccess: setQueue,
  });

  const rename = useMutation({
    mutationFn: (vars: { id: string; name: string }) =>
      updateBatch(repo, vars.id, { name: vars.name }),
    onSuccess: setQueue,
  });

  const dismiss = useMutation({
    mutationFn: (id: string) => dismissBatch(repo, id),
    onSuccess: (res) => {
      setQueue(res);
      setDismissId(null);
    },
  });
  const dismissTarget = batches.find((b) => b.id === dismissId);

  // Every batch gesture answers on the card that asked for it, so a refusal
  // lands where the user pressed.
  const batchError = (id: string): unknown => {
    if (launchBatch.variables === id) return launchBatch.error;
    if (rename.variables?.id === id) return rename.error;
    if (dismiss.variables === id) return dismiss.error;
    return null;
  };

  const executable = queueExecutable(builder.queue);
  const runnable = queueRunnable(builder.queue);

  const busy =
    move.isPending ||
    remove.isPending ||
    runOne.isPending ||
    add.isPending ||
    addAll.isPending ||
    runNext.isPending ||
    launchBatch.isPending ||
    rename.isPending ||
    dismiss.isPending;

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

  const toggleSelect = (id: string) =>
    setSelected((prev) =>
      prev.includes(id) ? prev.filter((s) => s !== id) : [...prev, id],
    );

  const toggleBatchExpand = (id: string) =>
    setExpandedBatchIds((prev) =>
      prev.includes(id) ? prev.filter((e) => e !== id) : [...prev, id],
    );

  return (
    <div className="flex max-w-page flex-col gap-6">
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
                <Input
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
                  className="h-auto w-56 px-2.5 py-1.5 font-mono text-sm placeholder:text-muted-foreground/60"
                />
                <Button
                  type="button"
                  variant={addState.confirmed ? "outline" : "default"}
                  size="sm"
                  className="font-mono"
                  onClick={fetchTicket}
                  disabled={issue.isFetching || draft.trim() === ""}
                >
                  {issue.isFetching ? "Fetching…" : "Fetch ticket"}
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="font-mono"
                  onClick={() => setBrowseOpen(true)}
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
                    disabled={addAll.isPending}
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
              {runOne.error ? (
                <p className="font-mono text-xs text-fail" role="alert">
                  {actionError(runOne.error)}
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
                      {ticket.id}
                      {ticket.project ? ` (project ${ticket.project})` : ""} is
                      not on {repo}'s board. Switch to the repo that owns it to
                      run it.
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
                {startRepo.choose && (
                  <MemberRepoField
                    members={startRepo.members}
                    value={startRepo.target}
                    suggested={startRepo.suggested}
                    picked={startRepo.picked}
                    ticket={submittedId}
                    onPick={startRepo.pick}
                  />
                )}
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
                <RunOptions
                  skips={addSkips}
                  onChange={setAddSkips}
                  open={addSkipsOpen}
                  onOpenChange={setAddSkipsOpen}
                />
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
                      disabled={runNext.isPending || add.isPending}
                    >
                      {runNext.isPending ? "Starting…" : "Run next"}
                    </Button>
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      className="font-mono"
                      onClick={() => add.mutate()}
                      disabled={add.isPending || runNext.isPending}
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

            {batches.length > 0 ? (
              <div className="flex flex-col gap-2">
                <span className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
                  batches
                </span>
                {batches.map((b) => (
                  <BatchCard
                    key={b.id}
                    batch={b}
                    summary={batchSummary(items, b.id)}
                    members={batchMembers(items, b.id)}
                    expanded={expandedBatchIds.includes(b.id)}
                    blocker={
                      queue.data ? batchStartBlocker(queue.data, b.id) : ""
                    }
                    busy={busy}
                    starting={
                      launchBatch.isPending && launchBatch.variables === b.id
                    }
                    error={batchError(b.id)}
                    onToggle={() => toggleBatchExpand(b.id)}
                    onStart={() => launchBatch.mutate(b.id)}
                    onRename={(name) => rename.mutate({ id: b.id, name })}
                    onDismiss={() => setDismissId(b.id)}
                    onPeek={onPeek}
                  />
                ))}
                <p className="font-sans text-xs leading-relaxed text-muted-foreground">
                  A batch runs its members in queue order and stops there — the
                  on-fault and skip-resume settings below apply to it too.
                </p>
              </div>
            ) : null}

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
                      batch={batchName(batches, item.batch ?? "")}
                      selected={picked.includes(item.id)}
                      onSelect={() => toggleSelect(item.id)}
                      onToggle={() => toggleExpand(item.id)}
                      onMove={(dir) => move.mutate({ id: item.id, dir })}
                      onRun={() => runOne.mutate(item.id)}
                      onRemove={askRemove}
                      onPeek={onPeek}
                    />
                  ))}
                </ul>
                <div className="flex flex-wrap items-center justify-between gap-2 border-t border-border bg-secondary/40 px-3 py-2 font-mono text-xs text-muted-foreground">
                  <span>
                    {builder.queue.length} queued · {executable} executable{" "}
                    {executable === 1 ? "ticket" : "tickets"} · runs top to
                    bottom
                  </span>
                  {picked.length > 0 ? (
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      className="font-mono"
                      onClick={() => {
                        newBatch.reset();
                        setBatchOpen(true);
                      }}
                    >
                      <Layers className="size-4" aria-hidden="true" />
                      New batch ({picked.length})
                    </Button>
                  ) : null}
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
              disabled={!runnable || start.isPending}
              title={runnable ? undefined : QUEUE_NOT_RUNNABLE}
            >
              {start.isPending ? "Starting…" : "Start queue"}
            </Button>
            {start.error ? (
              <p className="font-mono text-xs text-destructive">
                {actionError(start.error)}
              </p>
            ) : !runnable ? (
              <p className="font-mono text-xs text-muted-foreground">
                {QUEUE_NOT_RUNNABLE}
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

        <RemoveFromQueueDialog
          item={removeTarget}
          onOpenChange={(open) => {
            if (!open) setRemoveId(null);
          }}
          onConfirm={(item) => {
            remove.mutate(item);
            setRemoveId(null);
          }}
        />

        {batchOpen ? (
          <NewBatchDialog
            count={picked.length}
            pending={newBatch.isPending}
            error={newBatch.error}
            onOpenChange={(open) => {
              if (!open) setBatchOpen(false);
            }}
            onCreate={(name) => newBatch.mutate(name)}
          />
        ) : null}

        {dismissTarget ? (
          <ConfirmDialog
            open
            onOpenChange={(open) => {
              if (!open) setDismissId(null);
            }}
            windowTitle="dismiss batch"
            title={`Dismiss ${batchDisplayName(dismissTarget)}?`}
            description="The grouping goes and its card with it. Every item stays queued exactly where it is, and a run in flight is untouched."
            confirmLabel="Dismiss"
            onConfirm={() => dismiss.mutate(dismissTarget.id)}
          />
        ) : null}
      </TerminalCard>

      {builder.settled.length > 0 ? (
        <FinishedSection
          repo={repo}
          settled={builder.settled}
          itemsById={itemsById}
          busy={busy}
          onRemove={askRemove}
          onPeek={onPeek}
        />
      ) : null}

      {runSteps.dialog}
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
  item,
  busy,
  onRemove,
  onPeek,
}: {
  repo: string;
  ticket: TimelineTicket;
  item?: QueueItem;
  busy: boolean;
  onRemove: (id: string) => void;
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
  const body = ticket.hasRun ? (
    <Link
      to="/runs/$repo/$ticket"
      params={{ repo, ticket: ticket.id }}
      className="flex min-w-0 flex-1 flex-col gap-1.5 px-4 py-2.5 transition-colors hover:bg-secondary/40"
    >
      {head}
      {reason}
    </Link>
  ) : (
    <div className="flex min-w-0 flex-1 flex-col gap-1.5 px-4 py-2.5">
      {head}
      {reason}
    </div>
  );

  return (
    <li className="flex items-center border-b border-border/60 last:border-0">
      {body}
      {item ? (
        <span className="flex pr-3">
          <RemoveFromQueueButton
            item={item}
            disabled={busy}
            onRemove={onRemove}
          />
        </span>
      ) : null}
    </li>
  );
}

function FinishedSection({
  repo,
  settled,
  itemsById,
  busy,
  onRemove,
  onPeek,
}: {
  repo: string;
  settled: TimelineTicket[];
  itemsById: Map<string, QueueItem>;
  busy: boolean;
  onRemove: (id: string) => void;
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
                  item={itemsById.get(ticket.id)}
                  busy={busy}
                  onRemove={onRemove}
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

// LiveProgress is a live row's progress line: a base sync in flight names itself
// and its attempt counter, every other phase walks the generic stepper, restated
// against the work the queued item bypassed so this page and the run detail say
// the same thing about a skipped Step.
function LiveProgress({
  phase,
  activity,
  detail,
  skips,
}: {
  phase: string;
  activity?: string;
  detail?: string;
  skips?: string[];
}) {
  const sync = syncState(activity, detail);
  if (sync) return <SyncStateLine sync={sync} />;
  const { steps, subLabel } = runSteps("live", phase, activity, detail);
  return <PhaseStepper steps={withSkips(steps, skips)} subLabel={subLabel} />;
}

function RunningRow({
  repo,
  ticket,
  item,
  instance,
  now,
  busy,
  onRemove,
  onPeek,
}: {
  repo: string;
  ticket: TimelineTicket;
  item?: QueueItem;
  instance?: Instance;
  now: number;
  busy: boolean;
  onRemove: (id: string) => void;
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
        <BacklogPRBadge status={ticket.prStatus} />
        {item ? (
          <span className="ml-auto flex">
            <RemoveFromQueueButton
              item={item}
              disabled={busy}
              onRemove={onRemove}
            />
          </span>
        ) : null}
      </div>
      {phase || live?.activity ? (
        <LiveProgress
          phase={phase ?? ""}
          activity={live?.activity}
          detail={live?.detail}
          skips={item?.skips}
        />
      ) : (
        <p className="font-sans text-sm text-muted-foreground">
          Picking the next ticket…
        </p>
      )}
      <div className="flex flex-wrap items-center gap-x-6 gap-y-2 font-mono text-xs text-muted-foreground">
        <RunPaneLink repo={repo} ticket={ticket.id} pane="terminal">
          Terminal
        </RunPaneLink>
        <RunPaneLink repo={repo} ticket={ticket.id} pane="diff">
          Diff
        </RunPaneLink>
        {live ? (
          <>
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
          </>
        ) : null}
      </div>
    </div>
  );
}

function RunPaneLink({
  repo,
  ticket,
  pane,
  children,
}: {
  repo: string;
  ticket: string;
  pane: PaneTab;
  children: string;
}) {
  return (
    <Link
      to="/live/$repo/$ticket"
      params={{ repo, ticket }}
      search={{ pane }}
      className="inline-flex items-center gap-1.5 text-teal underline-offset-4 hover:underline"
    >
      <ExternalLink className="size-3.5" aria-hidden="true" />
      {children}
    </Link>
  );
}

const RELEASE_TONE: Partial<Record<RunState, string>> = {
  active: "border-teal/40 bg-teal/5",
  warn: "border-warn/40 bg-warn/5",
  info: "border-info/40 bg-info/5",
  fail: "border-fail/40 bg-fail/5",
};

// FinalizeRow takes the running row's place while the epic ships itself to its
// base. It reads the epic's own checkpoint, so a release parked for a human — or
// halted by one — still holds the row, wearing that state instead of live ticks.
function FinalizeRow({
  finalize,
  item,
  instance,
  now,
  onPeek,
}: {
  finalize: FinalizeEntry;
  item?: QueueItem;
  instance?: Instance;
  now: number;
  onPeek: (id: string) => void;
}) {
  const halted = finalize.failureClass !== undefined;
  const parked = !halted && isAwaitingHuman(finalize.release);
  const pill = finalizePill(finalize);
  const live = instance?.ticket === finalize.epicId ? instance : undefined;
  return (
    <div
      className={cn(
        "flex flex-col gap-3 rounded-md border px-4 py-3",
        RELEASE_TONE[pill.state],
      )}
    >
      <div className="flex flex-wrap items-center gap-3">
        <span className="font-sans text-base text-foreground">
          Releasing epic
        </span>
        <span className="inline-flex shrink-0 items-center gap-1 font-mono text-sm text-info">
          <span aria-hidden="true">◆</span>
          <TicketIdButton
            id={finalize.epicId}
            onPeek={onPeek}
            className="text-info"
          />
        </span>
        {finalize.title ? (
          <span className="min-w-0 truncate font-sans text-base text-foreground">
            {finalize.title}
          </span>
        ) : null}
        <InternalTag source={finalize.source} />
        <StatusPill
          state={pill.state}
          label={pill.label}
          className="shrink-0"
        />
      </div>
      {halted ? (
        finalize.reason ? <TicketReason>{finalize.reason}</TicketReason> : null
      ) : parked ? (
        <p className="font-sans text-sm text-muted-foreground">
          Every ticket is done — the epic PR is ready and yours to merge.
          {finalize.prUrl ? (
            <>
              {" "}
              <a
                href={finalize.prUrl}
                target="_blank"
                rel="noreferrer"
                className="font-mono text-xs text-primary underline-offset-2 hover:underline"
              >
                {finalize.prUrl}
              </a>
            </>
          ) : null}
        </p>
      ) : (
        <LiveProgress
          phase={finalize.phase}
          activity={finalize.activity}
          detail={finalize.detail}
          skips={item?.skips}
        />
      )}
      {live?.state_since ? (
        <div className="font-mono text-xs text-muted-foreground">
          in step{" "}
          <span className="text-foreground">
            {elapsedSince(live.state_since, now)}
          </span>
        </div>
      ) : null}
    </div>
  );
}

// RemainingAction is the gesture a remaining row offers: move it to the front of
// the queue so the drain picks it when the run in flight settles. A paused row
// promotes the same way — Start re-attempts it from there.
function RemainingAction({
  id,
  first,
  busy,
  onRunNext,
}: {
  id: string;
  first?: boolean;
  busy: boolean;
  onRunNext: (id: string) => void;
}) {
  return (
    <button
      type="button"
      onClick={() => onRunNext(id)}
      disabled={busy || first}
      title="Run next"
      aria-label={`Run next ${id}`}
      className="flex size-6 shrink-0 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-secondary hover:text-primary disabled:pointer-events-none disabled:opacity-30"
    >
      <ChevronsUp className="size-3.5" aria-hidden="true" />
    </button>
  );
}

function PendingTicketRow({
  ticket,
  item,
  first,
  busy,
  onPeek,
  onRunNext,
  onRemove,
}: {
  ticket: TimelineTicket;
  item?: QueueItem;
  first?: boolean;
  busy: boolean;
  onPeek: (id: string) => void;
  onRunNext?: (id: string) => void;
  onRemove: (id: string) => void;
}) {
  const pill = ticketPill(ticket);
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
      <ProviderTag provider={ticket.provider} pin={ticket.providerPin} />
      <BacklogPRBadge status={ticket.prStatus} />
      <StatusPill state={pill.state} label={pill.label} className="shrink-0" />
      {onRunNext ? (
        <RemainingAction
          id={ticket.id}
          first={first}
          busy={busy}
          onRunNext={onRunNext}
        />
      ) : null}
      {item ? (
        <RemoveFromQueueButton item={item} disabled={busy} onRemove={onRemove} />
      ) : null}
    </li>
  );
}

function PendingEpicGroup({
  entry,
  item,
  first,
  busy,
  onPeek,
  onRunNext,
  onRemove,
}: {
  entry: Extract<PendingEntry, { kind: "epic" }>;
  item?: QueueItem;
  first?: boolean;
  busy: boolean;
  onPeek: (id: string) => void;
  onRunNext?: (id: string) => void;
  onRemove: (id: string) => void;
}) {
  const paused = item?.status === "paused";
  return (
    <li className="border-b border-border/60 last:border-0">
      <div
        className={cn(
          "flex items-center gap-3 px-4 py-2.5",
          entry.active && "bg-teal/5",
        )}
      >
        <span className="inline-flex shrink-0 items-center gap-1 font-mono text-sm text-info">
          <span aria-hidden="true">◆</span>
          <TicketIdButton id={entry.id} onPeek={onPeek} className="text-info" />
        </span>
        <span className="min-w-0 flex-1 truncate font-sans text-sm text-foreground">
          {entry.title || "—"}
        </span>
        <InternalTag source={entry.source} />
        <StatusPill
          state={paused ? "warn" : "info"}
          label={
            paused
              ? `epic · paused · ${entry.done}/${entry.total}`
              : `epic · ${entry.done}/${entry.total}`
          }
          className="shrink-0"
        />
        {onRunNext ? (
          <RemainingAction
            id={entry.id}
            first={first}
            busy={busy}
            onRunNext={onRunNext}
          />
        ) : null}
        {item ? (
          <RemoveFromQueueButton
            item={item}
            disabled={busy}
            onRemove={onRemove}
          />
        ) : null}
      </div>
      {entry.children.length > 0 ? (
        <ul className="border-t border-border/60 bg-secondary/20">
          {entry.children.map((child) => {
            const pill = ticketPill(child);
            return (
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
                <StatusPill
                  state={pill.state}
                  label={pill.label}
                  className="shrink-0"
                />
              </li>
            );
          })}
        </ul>
      ) : null}
    </li>
  );
}

function drainStep(timeline: Timeline, batch: string): string {
  const draining = batch ? `draining batch ${batch}` : "draining";
  if (timeline.running) {
    return (
      stepName(
        timeline.running.activity,
        timeline.running.phase ?? "",
      ).toLowerCase() || draining
    );
  }
  return timeline.finalize ? "releasing" : draining;
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
  onPeek: (id: string) => void;
}) {
  const now = useNow(1000);
  const queryClient = useQueryClient();
  const [addOpen, setAddOpen] = useState(false);
  const [runNextId, setRunNextId] = useState<string | null>(null);
  const [removeId, setRemoveId] = useState<string | null>(null);

  const promote = useMutation({
    mutationFn: (id: string) => promoteQueueItem(repo, id),
    onSuccess: (res) => publishQueue(queryClient, repo, res),
  });
  const askRunNext = (id: string) => {
    promote.reset();
    setRunNextId(id);
  };
  const itemsById = new Map(queue.items.map((it) => [it.id, it]));
  const runNextTarget = runNextId ? itemsById.get(runNextId) : undefined;

  const remove = useMutation({
    mutationFn: (item: QueueItem) =>
      dequeue(repo, item.id, { stop: item.status === "running" }),
    onSuccess: (res) => publishQueue(queryClient, repo, res),
  });
  const askRemove = (id: string) => {
    remove.reset();
    setRemoveId(id);
  };
  const removeTarget = removeId ? itemsById.get(removeId) : undefined;
  const rowBusy = promote.isPending || remove.isPending;

  // The running row's queue entry is the epic when the drain is working one of
  // its sub-issues, since that is the row a removal drops.
  const running = timeline.running;
  const runningItem = running
    ? itemsById.get(running.epicId ?? running.id)
    : undefined;
  const batch = batchName(queue.batches, queue.draining_batch ?? "");
  // The releasing epic's own finalize is the one run the gate lets through, so
  // the wait reads as a wait only while nothing is in flight.
  const gate = running || timeline.finalize ? "" : releaseGateLabel(queue);
  const held = spawnHoldReason(queue);

  return (
    <div className="flex flex-col gap-6">
      <LoopBanner
        repo={repo}
        takeover={takeover}
        halt={halt}
        held={held}
        heldSince={queue.held_since}
      />

      <TerminalCard title="loop" className="max-w-page">
        <div className="flex flex-col gap-6">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="flex flex-wrap items-center gap-3">
              <span className="font-mono text-sm text-muted-foreground">
                <span className="text-foreground">
                  {timeline.done}/{timeline.total}
                </span>{" "}
                tickets done
              </span>
              {batch ? <BatchChip name={batch} /> : null}
            </div>
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
                state={gate || held ? "warn" : "active"}
                label={gate || (held ? "spawn held" : drainStep(timeline, batch))}
              />
            </div>
          </div>

          <section className="flex flex-col gap-2">
            <Eyebrow glyph="active">RUNNING</Eyebrow>
            {timeline.running ? (
              <RunningRow
                repo={repo}
                ticket={timeline.running}
                item={runningItem}
                instance={instance}
                now={now}
                busy={remove.isPending}
                onRemove={askRemove}
                onPeek={onPeek}
              />
            ) : timeline.finalize ? (
              <FinalizeRow
                finalize={timeline.finalize}
                item={itemsById.get(timeline.finalize.epicId)}
                instance={instance}
                now={now}
                onPeek={onPeek}
              />
            ) : gate ? (
              <p className="font-sans text-sm text-muted-foreground">
                Nothing new starts while {queue.releasing_epic} is releasing —
                the queue picks up once its release lands or is handed off.
              </p>
            ) : held ? (
              <p className="font-sans text-sm text-muted-foreground">
                Nothing new starts while the drain is held.
              </p>
            ) : (
              <p className="font-sans text-sm text-muted-foreground">
                Idle — picking the next ticket from the queue.
              </p>
            )}
            {remove.error ? (
              <p className="font-mono text-xs text-fail" role="alert">
                {actionError(remove.error)}
              </p>
            ) : null}
          </section>

          <section className="flex flex-col gap-2">
            <div className="flex items-center justify-between gap-3">
              <Eyebrow glyph="idle">REMAINING</Eyebrow>
              <button
                type="button"
                onClick={() => setAddOpen(true)}
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
                    {timeline.pending.map((entry, index) =>
                      entry.kind === "epic" ? (
                        <PendingEpicGroup
                          key={entry.id}
                          entry={entry}
                          item={itemsById.get(entry.id)}
                          first={index === 0}
                          busy={rowBusy}
                          onPeek={onPeek}
                          onRunNext={askRunNext}
                          onRemove={askRemove}
                        />
                      ) : (
                        <PendingTicketRow
                          key={entry.ticket.id}
                          ticket={entry.ticket}
                          item={itemsById.get(entry.ticket.id)}
                          first={index === 0}
                          busy={rowBusy}
                          onPeek={onPeek}
                          onRunNext={askRunNext}
                          onRemove={askRemove}
                        />
                      ),
                    )}
                  </ul>
                </div>
                {promote.error ? (
                  <p className="font-mono text-xs text-fail" role="alert">
                    {actionError(promote.error)}
                  </p>
                ) : null}
                <p className="font-sans text-xs leading-relaxed text-muted-foreground">
                  Remaining tickets run top to bottom — Run next moves one to the
                  front for when the current run finishes, and Remove takes one
                  out of the loop for good.
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
                  >
                    <Plus className="size-4" aria-hidden="true" />
                    Add ticket
                  </Button>
                }
              />
            )}
          </section>

          {timeline.outside.length > 0 ? (
            <section className="flex flex-col gap-2">
              <Eyebrow glyph="idle">
                OUTSIDE THIS BATCH · {timeline.outside.length}
              </Eyebrow>
              <div className="overflow-hidden rounded-md border border-border opacity-60">
                <ul className="flex flex-col">
                  {timeline.outside.map((entry) =>
                    entry.kind === "epic" ? (
                      <PendingEpicGroup
                        key={entry.id}
                        entry={entry}
                        item={itemsById.get(entry.id)}
                        busy={rowBusy}
                        onPeek={onPeek}
                        onRemove={askRemove}
                      />
                    ) : (
                      <PendingTicketRow
                        key={entry.ticket.id}
                        ticket={entry.ticket}
                        item={itemsById.get(entry.ticket.id)}
                        busy={rowBusy}
                        onPeek={onPeek}
                        onRemove={askRemove}
                      />
                    ),
                  )}
                </ul>
              </div>
              <p className="font-sans text-xs leading-relaxed text-muted-foreground">
                These stay queued — the batch runs its members, then the loop
                stops. Remove takes one out of the queue for good.
              </p>
            </section>
          ) : null}

          {timeline.finished.length > 0 ? (
            <FinishedSection
              repo={repo}
              settled={timeline.finished}
              itemsById={itemsById}
              busy={rowBusy}
              onRemove={askRemove}
              onPeek={onPeek}
            />
          ) : null}
        </div>
      </TerminalCard>

      <div className="flex max-w-page flex-wrap items-end justify-end gap-4">
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
                variant="outline"
                size="sm"
                className="font-mono"
                disabled={stopping}
              >
                <Square className="size-4" aria-hidden="true" />
                {stopping ? "Stopping…" : "Stop"}
              </Button>
            }
            title={`Stop the loop on ${repo}?`}
            description="The run stops now. Work in progress is saved at the last checkpoint and the item stays resumable — Start picks it up from there. Every other row stays queued."
            confirmLabel="Stop"
            destructive
            onConfirm={onStop}
          />
          <ActionCaption>
            Stops now; work is saved at the last checkpoint — resumable with
            Start.
          </ActionCaption>
        </div>
      </div>

      {runNextTarget ? (
        <ConfirmDialog
          open
          onOpenChange={(open) => {
            if (!open) setRunNextId(null);
          }}
          windowTitle="confirm"
          title={`Run ${runNextTarget.id} next?`}
          description={
            <>
              {runNextTarget.id} moves to the front of the remaining list. The
              run in flight is never interrupted — the change takes effect when
              the current run finishes. Running ahead of queue order is risky:
              later tickets may assume earlier queued work already landed.
              {runNextTarget.blocked ? (
                <span className="mt-2 block text-fail">
                  {runNextTarget.id} is blocked by{" "}
                  {(runNextTarget.blockers ?? []).join(", ")} — the drain will
                  run it anyway.
                </span>
              ) : null}
            </>
          }
          confirmLabel="Run next"
          destructive
          onConfirm={() => promote.mutate(runNextTarget.id)}
        />
      ) : null}

      <RemoveFromQueueDialog
        item={removeTarget}
        onOpenChange={(open) => {
          if (!open) setRemoveId(null);
        }}
        onConfirm={(item) => {
          remove.mutate(item);
          setRemoveId(null);
        }}
      />

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
  attribution?: HaltAttribution;
}

interface HaltAttribution {
  ticket: string;
  text: string;
}

const REASON_LEAD = /^(?:ticket\s+)?([A-Z][A-Z0-9]*-\d+)\b[\s:,–—-]*/;
const REASON_TICKET = /\b[A-Z][A-Z0-9]*-\d+\b/;

// haltAttribution names the ticket a stop reason blames when that is not the
// queue item itself, so the banner can relate the two ids rather than show one
// in its headline and another in its link with nothing tying them together.
function haltAttribution(halt: LoopHalt): HaltAttribution | undefined {
  const lead = halt.reason.match(REASON_LEAD);
  const ticket = lead?.[1] ?? halt.reason.match(REASON_TICKET)?.[0];
  if (!ticket || ticket === halt.ticket) return undefined;

  const tail = (lead ? halt.reason.slice(lead[0].length) : halt.reason).trim();
  const sub = halt.subTickets?.includes(ticket);
  const relation = sub ? `sub-ticket ${ticket}` : `while working ${ticket}`;
  const joiner = sub ? " " : ": ";
  return { ticket, text: tail ? `${relation}${joiner}${tail}` : relation };
}

export function haltNotice(halt: LoopHalt): HaltNotice {
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
      switch (pauseKind(halt.reason)) {
        case "reauth":
          return {
            tone: "warn",
            glyph: "⚠",
            headline: "paused — re-authentication needed",
            hint: "This is not a failure. Re-login to the provider, then the queue resumes.",
          };
        case "usage_window":
          return {
            tone: "warn",
            glyph: "⚠",
            headline: "paused — rate limit reached",
            hint: "This is not a failure. The queue resumes on its own once the provider's usage window clears.",
          };
        default: {
          const attribution = haltAttribution(halt);
          const subject = attribution ? halt.ticket : halt.reason;
          return {
            tone: "warn",
            glyph: "⚠",
            headline: subject ? `paused — ${subject}` : "paused",
            hint: "This is not a failure. Clear what the run is waiting on, then Start to re-attempt this item.",
            attribution,
          };
        }
      }
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
        hint: `${ticket} needs a human — open the run to see why, then Requeue to retry it from scratch once the cause is fixed.`,
      };
  }
}

function TakeoverBanner({ repo, ticket }: { repo: string; ticket?: string }) {
  return (
    <div className="flex max-w-page items-start gap-2.5 rounded-lg border border-info/40 bg-info/10 px-4 py-3">
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

// LoopBanner is the page's single headline slot, and the live states own it: a
// takeover first, then a drain holding its next spawn. A halt stored behind
// either is history rather than a second, contradicting banner.
function LoopBanner({
  repo,
  takeover,
  halt,
  held,
  heldSince,
}: {
  repo: string;
  takeover?: Instance;
  halt: LoopHalt | null;
  held: string;
  heldSince?: string;
}) {
  if (takeover) return <TakeoverBanner repo={repo} ticket={takeover.ticket} />;
  if (held) return <HeldBanner reason={held} since={heldSince} />;
  if (halt) return <HaltBanner repo={repo} halt={halt} />;
  return null;
}

// HeldBanner names what an armed drain is waiting on and how long it has waited,
// so a queue that starts nothing reads as a wait with a reason rather than as a
// hang nobody can tell from a crash.
function HeldBanner({ reason, since }: { reason: string; since?: string }) {
  const now = useNow(30_000);
  return (
    <div
      role="status"
      className="flex max-w-page items-start gap-2.5 rounded-lg border border-warn/40 bg-warn/10 px-4 py-3"
    >
      <TriangleAlert
        className="mt-0.5 size-4 shrink-0 text-warn"
        aria-hidden="true"
      />
      <div className="flex flex-col gap-1">
        <span className="font-mono text-sm text-warn">
          Spawn held{since ? ` for ${elapsedSince(since, now)}` : ""}
        </span>
        <p className="font-sans text-sm leading-relaxed text-foreground">
          The queue is draining but nothing new is starting — {reason}.
        </p>
      </div>
    </div>
  );
}

function HaltBanner({ repo, halt }: { repo: string; halt: LoopHalt }) {
  const notice = haltNotice(halt);
  const { border, bg, text: glyphColor } = HALT_TONE[notice.tone];
  const tickets = [halt.ticket, notice.attribution?.ticket].filter(
    (t): t is string => !!t,
  );
  return (
    <div
      className={cn(
        "flex max-w-page items-start gap-2.5 rounded-lg border px-4 py-3",
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
        {notice.attribution ? (
          <TicketReason>{notice.attribution.text}</TicketReason>
        ) : null}
        <p className="font-sans text-sm leading-relaxed text-foreground">
          {notice.hint}
        </p>
        {tickets.length ? (
          <div className="mt-1 flex flex-wrap items-center gap-x-4 gap-y-1">
            {tickets.map((ticket) => (
              <HaltLink key={ticket} repo={repo} ticket={ticket} />
            ))}
            {halt.ticket &&
            (halt.kind === "quarantined" || halt.kind === "fault") ? (
              <ProposeFixAction repo={repo} ticket={halt.ticket} />
            ) : null}
            {halt.kind === "quarantined" && halt.ticket ? (
              <RequeueAction repo={repo} ticket={halt.ticket} />
            ) : null}
          </div>
        ) : null}
      </div>
    </div>
  );
}

// RequeueAction is the quarantine banner's way back: one click drives the same
// trau --requeue the CLI offers and republishes the repaired queue, so the
// banner clears and Start can re-attempt the ticket. The runs query is
// invalidated alongside — the requeue dropped the checkpoint the settled run
// row was derived from.
function RequeueAction({ repo, ticket }: { repo: string; ticket: string }) {
  const queryClient = useQueryClient();
  const requeue = useMutation({
    mutationFn: () => requeueIssue(repo, ticket),
    onSuccess: (res) => {
      publishQueue(queryClient, repo, res);
      queryClient.invalidateQueries({ queryKey: ["runs", repo] });
    },
  });
  return (
    <>
      <button
        type="button"
        onClick={() => requeue.mutate()}
        disabled={requeue.isPending}
        className="inline-flex w-fit cursor-pointer items-center gap-1.5 font-mono text-xs text-teal underline-offset-4 hover:underline disabled:cursor-default disabled:opacity-60"
      >
        <RefreshCw
          className={cn("size-3.5", requeue.isPending && "animate-spin")}
          aria-hidden="true"
        />
        {requeue.isPending ? `Requeuing ${ticket}…` : `Requeue ${ticket}`}
      </button>
      {requeue.isError ? (
        <span className="font-sans text-xs text-fail">
          {requeue.error instanceof Error
            ? requeue.error.message
            : "requeue failed"}
        </span>
      ) : null}
    </>
  );
}

// ProposeFixAction is the halt banner's other way forward: instead of re-running the
// same attempt, it opens an Inbox session that diagnoses the failure from the run's
// dossier and rewrites the ticket for the next one.
function ProposeFixAction({ repo, ticket }: { repo: string; ticket: string }) {
  const navigate = useNavigate();
  const proposeFix = useProposeFix({
    repo,
    ticket,
    enabled: true,
    onStarted: () =>
      void navigate({ to: "/inbox", search: { issue: ticket, repo } }),
  });
  if (proposeFix.session) {
    return (
      <Link
        to="/inbox"
        search={{ issue: ticket, repo }}
        className="inline-flex w-fit items-center gap-1.5 font-mono text-xs text-teal underline-offset-4 hover:underline"
      >
        <Wrench className="size-3.5" aria-hidden="true" />
        Open fix session
      </Link>
    );
  }
  return (
    <>
      <button
        type="button"
        onClick={proposeFix.start}
        disabled={proposeFix.starting}
        className="inline-flex w-fit cursor-pointer items-center gap-1.5 font-mono text-xs text-teal underline-offset-4 hover:underline disabled:cursor-default disabled:opacity-60"
      >
        <Wrench
          className={cn("size-3.5", proposeFix.starting && "animate-pulse")}
          aria-hidden="true"
        />
        {proposeFix.starting ? "Starting…" : `Propose fix for ${ticket}`}
      </button>
      {proposeFix.error ? (
        <span className="font-sans text-xs text-fail">{proposeFix.error}</span>
      ) : null}
    </>
  );
}

function HaltLink({ repo, ticket }: { repo: string; ticket: string }) {
  return (
    <Link
      to="/live/$repo/$ticket"
      params={{ repo, ticket }}
      className="inline-flex w-fit items-center gap-1.5 font-mono text-xs text-teal underline-offset-4 hover:underline"
    >
      <ExternalLink className="size-3.5" aria-hidden="true" />
      Open {ticket}
    </Link>
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
  batch: string,
): LoopTitleState {
  if (!canRun) return { kind: "idle" };
  if (halt) return { kind: "halted", halt: halt.kind, ticket: halt.ticket };
  if (view === "running" && timeline) {
    return {
      kind: "draining",
      done: timeline.done,
      total: timeline.total,
      ticket: timeline.running?.id ?? timeline.finalize?.epicId ?? "",
      step: drainStep(timeline, batch),
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

  const queue = useQuery({
    ...queueQueryOptions(repo),
    refetchInterval: (q) => (queueLive(q.state.data) ? 3000 : false),
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
  const drainingBatch = batchName(
    queue.data?.batches,
    queue.data?.draining_batch ?? "",
  );
  usePageTitle(
    loopTitle(loopTitleState(canRun, halt, view, timeline, drainingBatch)),
  );

  const stop = useMutation({
    mutationFn: () => stopQueue(repo),
    onSuccess: (res) => publishQueue(queryClient, repo, res),
  });

  useEffect(() => {
    stop.reset();
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

  // Keyed on the armed drain rather than the running view: a held or halted
  // drain is exactly when a pasted terminal agent earns its keep.
  const babysitter = queue.data?.draining ? (
    <BabysitterCard
      repo={repo}
      root={repos.find((r) => r.name === repo)?.root}
    />
  ) : null;

  if (view === "running" && queue.data && timeline) {
    return (
      <>
        <div className="flex flex-col gap-6">
          <RunningQueueView
            repo={repo}
            queue={queue.data}
            timeline={timeline}
            instance={liveInstance}
            takeover={takeoverInstance}
            halt={halt}
            onStop={() => stop.mutate()}
            stopping={stop.isPending || queue.data.stopping}
            stopError={stop.error}
            onPeek={onPeek}
          />
          {babysitter}
        </div>
        {drawer}
      </>
    );
  }

  return (
    <div className="flex flex-col gap-6">
      <LoopBanner
        repo={repo}
        takeover={takeoverInstance}
        halt={halt}
        held={spawnHoldReason(queue.data)}
        heldSince={queue.data?.held_since}
      />
      <LaunchQueueCard
        repo={repo}
        freshness={repos.find((r) => r.name === repo)?.freshness}
        onPeek={onPeek}
      />
      {babysitter}
      {drawer}
    </div>
  );
}

function NotStartableNotice({ repo, root }: { repo: string; root?: string }) {
  return (
    <TerminalCard title="loop" className="max-w-page">
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
