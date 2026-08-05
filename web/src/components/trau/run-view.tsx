import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { parseAsStringLiteral, useQueryState } from "nuqs";
import {
  ChevronRight,
  ExternalLink,
  Loader2,
  ScrollText,
  Square,
  SquareTerminal,
  Wrench,
} from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/trau/confirm-dialog";
import { Eyebrow, type EyebrowGlyph } from "@/components/trau/eyebrow";
import { NoSkillsBanner } from "@/components/trau/no-skills-banner";
import { NoBrowserBanner } from "@/components/trau/no-browser-banner";
import { OpenInEditor } from "@/components/trau/open-in-editor";
import { PhaseStepper } from "@/components/trau/phase-stepper";
import { PRStatusBadge } from "@/components/trau/pr-status-badge";
import { RunDiff } from "@/components/trau/run-diff";
import {
  proposeFixable,
  useProposeFix,
} from "@/components/grill/propose-fix";
import { RunPageHeader } from "@/components/trau/run-page-header";
import {
  SegmentedControl,
  type SegmentOption,
} from "@/components/trau/segmented-control";
import { SteerComposer } from "@/components/trau/steer-notes";
import { StatusPill } from "@/components/trau/status-pill";
import { SyncStateBanner } from "@/components/trau/sync-state";
import { TerminalCard } from "@/components/trau/terminal-card";
import { Terminal } from "@/components/terminal";
import { cn } from "@/lib/utils";
import { useNow } from "@/lib/elapsed";
import { useEventFeed, type FeedEvent } from "@/lib/events";
import { runTitle, usePageTitle } from "@/lib/page-title";
import { runCheckpointQueryOptions } from "@/lib/checkpoints";
import {
  instancesQueryOptions,
  repoRoot,
  repoTakenOver,
  stopInstance,
  takeoverRun,
  TakeoverError,
  type Instance,
} from "@/lib/instances";
import { sessionStatePill } from "@/lib/overview";
import { phaseLogsQueryOptions, type PhaseLog } from "@/lib/phaselogs";
import { runDetailQueryOptions, type RunDetail } from "@/lib/rundetail";
import { isSyncLog, syncState, type SyncState } from "@/lib/steps";
import {
  deriveElapsedMs,
  deriveVariant,
  formatCostUSD,
  formatDuration,
  formatTokens,
  headerPill,
  pauseBanner,
  phaseLabel,
  runSteps,
  STOPPED_HEADLINE,
  STOPPED_HINT,
  sumCosts,
  type RunVariant,
} from "@/lib/runlive";

// A backgrounded tab throttles interval polls to ~1/min, so the tab title would
// lag phase transitions. These feed kinds mark a pipeline-phase move (not churny
// per-line agent activity); one arriving forces an immediate registry, run, and
// diff refetch so the title and the changed files snap. Everything else rides the
// normal poll.
const PHASE_EVENT_KINDS = new Set([
  "agent_start",
  "activity_change",
  "pr_open",
  "state_change",
]);

export const PANE_VALUES = ["terminal", "diff"] as const;

export type PaneTab = (typeof PANE_VALUES)[number];

const PANE_OPTIONS: readonly SegmentOption<PaneTab>[] = [
  { value: "terminal", label: "Terminal" },
  { value: "diff", label: "Diff" },
];

const paneParser = parseAsStringLiteral(PANE_VALUES).withDefault("terminal");

function elapsedSince(fromISO: string, now: number): string {
  return formatDuration(Math.max(0, now - new Date(fromISO).getTime()));
}

function clock(ts: string): string {
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

function fieldStr(ev: FeedEvent, key: string): string {
  const v = ev.fields?.[key];
  return typeof v === "string" ? v : "";
}

function fieldNum(ev: FeedEvent, key: string): number | undefined {
  const v = ev.fields?.[key];
  return typeof v === "number" ? v : undefined;
}

interface ActivityRow {
  glyph: string;
  glyphClass: string;
  text: string;
}

function activityRow(ev: FeedEvent): ActivityRow {
  switch (ev.kind) {
    case "agent_start":
      return {
        glyph: "▸",
        glyphClass: "text-primary",
        text: ev.phase ? `phase ${ev.phase} started` : "agent started",
      };
    case "agent_call": {
      if (ev.fields?.is_error === true) {
        const err = fieldStr(ev, "error");
        return {
          glyph: "✗",
          glyphClass: "text-fail",
          text: err
            ? `${fieldStr(ev, "provider")} error — ${err}`
            : "agent call failed",
        };
      }
      const provider = fieldStr(ev, "provider");
      const cost = fieldNum(ev, "cost_usd");
      if (cost) {
        return {
          glyph: "●",
          glyphClass: "text-teal",
          text: `$${cost.toFixed(2)} spent${provider ? ` (${provider})` : ""}`,
        };
      }
      const model = fieldStr(ev, "model");
      return {
        glyph: "●",
        glyphClass: "text-teal",
        text: [provider, model].filter(Boolean).join(" · ") || "agent call",
      };
    }
    case "usage_window": {
      const provider = fieldStr(ev, "provider");
      const label = fieldStr(ev, "label");
      const util = fieldNum(ev, "utilization");
      return {
        glyph: "●",
        glyphClass: "text-info",
        text:
          util !== undefined
            ? `${provider} ${label} ${Math.round(util)}%`.trim()
            : `${provider} usage`.trim(),
      };
    }
    case "cost_anomaly":
      return {
        glyph: "⚠",
        glyphClass: "text-warn",
        text: ev.msg || "cost anomaly",
      };
    case "build_no_skills":
      return {
        glyph: "⚠",
        glyphClass: "text-warn",
        text: ev.msg || "build loaded no skills",
      };
    case "skill_load_failed":
      return {
        glyph: "⚠",
        glyphClass: "text-warn",
        text: ev.msg || "a Skill call failed",
      };
    case "test_gate": {
      const failed = fieldStr(ev, "result") === "fail";
      return {
        glyph: failed ? "✗" : "✓",
        glyphClass: failed ? "text-fail" : "text-teal",
        text:
          ev.msg ||
          (failed
            ? "test gate failed — routed to repair without a verify turn"
            : "test gate passed"),
      };
    }
    case "verify_no_browser":
      return {
        glyph: "⚠",
        glyphClass: "text-warn",
        text: ev.msg || "browser verify skipped on a UI slice",
      };
    case "qa_roster":
      return {
        glyph: "●",
        glyphClass: "text-info",
        text: ev.msg || "QA roster checked for verify",
      };
    case "qa_captured":
      return {
        glyph: "✓",
        glyphClass: "text-teal",
        text: ev.msg || "QA account captured",
      };
    case "model_fallback":
      return {
        glyph: "⚠",
        glyphClass: "text-warn",
        text: ev.msg || "no model configured — ran on the built-in default",
      };
    case "spawn_failed":
      return {
        glyph: "✗",
        glyphClass: "text-fail",
        text: fieldStr(ev, "error") || ev.msg || "loop failed to start",
      };
    case "pr_open": {
      const n = fieldNum(ev, "number");
      return {
        glyph: "●",
        glyphClass: "text-info",
        text: n ? `PR #${n} opened` : "PR opened",
      };
    }
    case "state_change": {
      const state = fieldStr(ev, "state");
      const reason = fieldStr(ev, "reason");
      const glyphClass =
        state === "merged"
          ? "text-done"
          : state === "paused"
            ? "text-warn"
            : "text-fail";
      const glyph = state === "merged" ? "✓" : state === "paused" ? "⚠" : "✗";
      return {
        glyph,
        glyphClass,
        text: [fieldStr(ev, "ticket"), state, reason]
          .filter(Boolean)
          .join(" · "),
      };
    }
    default:
      return {
        glyph: "●",
        glyphClass: "text-muted-foreground",
        text: ev.msg || ev.kind,
      };
  }
}

function ActivityFeed({ events }: { events: FeedEvent[] }) {
  const [open, setOpen] = useState(false);
  const rows = events.slice(0, 40);
  const latest = events[0];
  const latestRow = latest ? activityRow(latest) : null;

  return (
    <section className="overflow-hidden rounded-lg border border-border bg-card">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className="flex w-full items-center justify-between gap-4 px-4 py-2.5 text-left transition-colors hover:bg-secondary/40"
      >
        <span className="flex min-w-0 items-center gap-3 font-mono text-xs">
          <ChevronRight
            className={cn(
              "size-3.5 shrink-0 text-muted-foreground transition-transform",
              open && "rotate-90",
            )}
            aria-hidden="true"
          />
          <span className="shrink-0 text-muted-foreground">
            activity ({events.length})
          </span>
          {!open && (
            <span className="flex min-w-0 items-center gap-2 text-faint">
              {latest && latestRow ? (
                <>
                  <span className="shrink-0 tabular-nums">
                    {clock(latest.ts)}
                  </span>
                  <span
                    className={cn("shrink-0", latestRow.glyphClass)}
                    aria-hidden="true"
                  >
                    {latestRow.glyph}
                  </span>
                  <span className="truncate text-muted-foreground">
                    {latestRow.text}
                  </span>
                </>
              ) : (
                <span className="truncate">waiting for activity…</span>
              )}
            </span>
          )}
        </span>
        <span className="shrink-0 font-mono text-[0.65rem] uppercase tracking-[0.18em] text-faint">
          {open ? "hide" : "show"}
        </span>
      </button>
      {open && (
        <div className="border-t border-border">
          {rows.length === 0 ? (
            <p className="px-4 py-6 text-center font-mono text-xs text-muted-foreground">
              Waiting for activity…
            </p>
          ) : (
            <ul className="flex flex-col">
              {rows.map((ev) => {
                const row = activityRow(ev);
                return (
                  <li
                    key={ev.id}
                    className="flex items-start gap-3 border-b border-border/60 px-4 py-3 font-mono text-xs last:border-0"
                  >
                    <span className="shrink-0 tabular-nums text-muted-foreground">
                      {clock(ev.ts)}
                    </span>
                    <span
                      className={cn("shrink-0", row.glyphClass)}
                      aria-hidden="true"
                    >
                      {row.glyph}
                    </span>
                    <span className="text-pretty text-foreground">
                      {row.text}
                    </span>
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      )}
    </section>
  );
}

function RecapRow({
  label,
  children,
  valueClassName,
}: {
  label: string;
  children: React.ReactNode;
  valueClassName?: string;
}) {
  return (
    <div className="flex items-center justify-between gap-4 border-b border-border/60 py-2.5 last:border-0">
      <span className="font-mono text-[0.7rem] uppercase tracking-[0.18em] text-muted-foreground">
        {label}
      </span>
      <span className={cn("font-mono text-sm text-foreground", valueClassName)}>
        {children}
      </span>
    </div>
  );
}

function Recap({
  run,
  variant,
  elapsed,
  actions,
}: {
  run: RunDetail;
  variant: RunVariant;
  elapsed: string | null;
  actions: React.ReactNode;
}) {
  const pill = headerPill(variant, run.phase, run.failure_class);
  const totals = sumCosts(run.costs);
  const failed = variant === "failure";
  return (
    <TerminalCard title="recap">
      <div className="flex flex-col gap-4">
        <div className="flex items-center gap-2">
          <StatusPill state={pill.state} label={pill.label} />
        </div>
        <div className="flex flex-col">
          <RecapRow
            label="phase reached"
            valueClassName={failed ? "text-fail" : "text-done"}
          >
            {phaseLabel(run.phase)}
          </RecapRow>
          {run.ships && run.ships.length > 1
            ? run.ships.map((ship) => (
                <RecapRow key={ship.repo} label={ship.repo}>
                  {ship.url ? (
                    <a
                      href={ship.url}
                      target="_blank"
                      rel="noreferrer"
                      className="text-teal underline-offset-4 hover:underline"
                    >
                      #{ship.pr}
                    </a>
                  ) : (
                    "branch pushed, no pr yet"
                  )}
                </RecapRow>
              ))
            : run.pr &&
              run.pr_url && (
                <RecapRow label="pr">
                  <a
                    href={run.pr_url}
                    target="_blank"
                    rel="noreferrer"
                    className="text-teal underline-offset-4 hover:underline"
                  >
                    #{run.pr}
                  </a>
                </RecapRow>
              )}
          {failed && run.failure_reason && (
            <RecapRow label="failure reason" valueClassName="text-fail">
              {run.failure_reason}
            </RecapRow>
          )}
          {run.artifacts.tokens && (
            <>
              <RecapRow label="tokens">{formatTokens(totals.tokens)}</RecapRow>
              <RecapRow label="cost">
                {formatCostUSD(totals.usd, totals.metered)}
              </RecapRow>
            </>
          )}
          {elapsed && <RecapRow label="elapsed">{elapsed}</RecapRow>}
        </div>
        <div className="flex flex-wrap items-center gap-2">{actions}</div>
      </div>
    </TerminalCard>
  );
}

function PausedBanner({ reason }: { reason: string }) {
  const banner = pauseBanner(reason);
  return (
    <div className="flex flex-col gap-1 rounded-lg border border-warn/40 bg-warn/10 px-4 py-3">
      <span className="inline-flex items-center gap-2 font-mono text-sm text-warn">
        <span aria-hidden="true">⚠</span>
        {banner.headline}
      </span>
      <p className="font-sans text-sm leading-relaxed text-muted-foreground">
        {banner.hint}
      </p>
    </div>
  );
}

function FailedToStartBanner({ error }: { error: string }) {
  return (
    <div className="flex flex-col gap-2 rounded-lg border border-fail/40 bg-fail/10 px-4 py-3">
      <span className="inline-flex items-center gap-2 font-mono text-sm text-fail">
        <span aria-hidden="true">✗</span>
        failed to start
      </span>
      <p className="font-sans text-sm leading-relaxed text-muted-foreground">
        The loop exited before it could run — it never registered or wrote a
        checkpoint. This is the error it reported:
      </p>
      <pre className="overflow-x-auto rounded-md border border-border bg-secondary/40 px-3 py-2 font-mono text-xs text-foreground">
        {error || "no error output was captured"}
      </pre>
    </div>
  );
}

function StoppedBanner() {
  return (
    <div className="flex flex-col gap-1 rounded-lg border border-info/40 bg-info/10 px-4 py-3">
      <span className="inline-flex items-center gap-2 font-mono text-sm text-info">
        <span aria-hidden="true">⏹</span>
        {STOPPED_HEADLINE}
      </span>
      <p className="font-sans text-sm leading-relaxed text-muted-foreground">
        {STOPPED_HINT}
      </p>
    </div>
  );
}

// SyncLogs renders what the conflict-resolution attempts reported — the files
// they resolved and how they verified the merge — once each attempt's phase log
// lands.
function SyncLogs({ logs }: { logs: PhaseLog[] }) {
  const [open, setOpen] = useState(false);

  return (
    <section className="overflow-hidden rounded-lg border border-border bg-card">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className="flex w-full items-center justify-between gap-4 px-4 py-2.5 text-left transition-colors hover:bg-secondary/40"
      >
        <span className="flex min-w-0 items-center gap-3 font-mono text-xs">
          <ChevronRight
            className={cn(
              "size-3.5 shrink-0 text-muted-foreground transition-transform",
              open && "rotate-90",
            )}
            aria-hidden="true"
          />
          <span className="shrink-0 text-muted-foreground">
            merge resolution ({logs.length})
          </span>
          {!open && (
            <span className="truncate text-faint">
              {logs.map((log) => log.phase).join(" · ")}
            </span>
          )}
        </span>
        <span className="shrink-0 font-mono text-[0.65rem] uppercase tracking-[0.18em] text-faint">
          {open ? "hide" : "show"}
        </span>
      </button>
      {open && (
        <ul className="flex flex-col border-t border-border">
          {logs.map((log) => (
            <li
              key={log.phase}
              className="flex flex-col gap-2 border-b border-border/60 px-4 py-3 last:border-0"
            >
              <span className="font-mono text-xs text-info">{log.phase}</span>
              <pre className="overflow-x-auto whitespace-pre-wrap rounded-md border border-border bg-secondary/40 px-3 py-2 font-mono text-xs text-foreground">
                {log.content.trim() || "no output was captured"}
              </pre>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

// MainPane stacks the run's transcript and its live diff behind one toggle. The
// terminal stays mounted and CSS-hidden rather than unmounted, so flipping to the
// diff never tears down its SSE transcript stream; the diff only mounts while it
// shows, which is what keeps its polling off the rest of the time.
function MainPane({
  repo,
  ticket,
  glyph,
  terminal,
  sync,
}: {
  repo: string;
  ticket: string;
  glyph: EyebrowGlyph;
  terminal: React.ReactNode;
  sync?: SyncState;
}) {
  const [tab, setTab] = useQueryState("pane", paneParser);
  const diff = tab === "diff";

  return (
    <div className="flex flex-1 flex-col gap-2">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <Eyebrow glyph={glyph}>{diff ? "DIFF" : "TERMINAL"}</Eyebrow>
        <SegmentedControl
          aria-label="Run pane"
          options={PANE_OPTIONS}
          value={tab}
          onChange={(value) => void setTab(value)}
        />
      </div>
      <div className={cn("flex flex-1 flex-col", diff && "hidden")}>
        {terminal}
      </div>
      {diff && <RunDiff repo={repo} ticket={ticket} sync={sync} />}
    </div>
  );
}

function StartingPlaceholder() {
  return (
    <div className="flex flex-col items-center justify-center gap-2 rounded-lg border border-border bg-card px-4 py-12 text-center">
      <span className="inline-flex items-center gap-2 font-mono text-sm text-teal">
        <span className="cursor-block" aria-hidden="true">
          ▍
        </span>
        starting…
      </span>
      <p className="max-w-md font-sans text-sm leading-relaxed text-muted-foreground">
        Launching the loop. The transcript appears here once this run's first
        agent session begins.
      </p>
    </div>
  );
}

export function RunView({ repo, ticket }: { repo: string; ticket: string }) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const now = useNow(1000);
  const [stopOpen, setStopOpen] = useState(false);
  const [takeoverUnsupported, setTakeoverUnsupported] = useState(false);

  const { data: instData } = useQuery(instancesQueryOptions);
  const { data: run } = useQuery(runDetailQueryOptions(repo, ticket));
  const { data: checkpoint } = useQuery(
    runCheckpointQueryOptions(repo, ticket),
  );
  const { data: phaseLogs } = useQuery(phaseLogsQueryOptions(repo, ticket));
  const feed = useEventFeed(repo);

  const instance: Instance | undefined = instData?.instances.find(
    (i) => i.repo === repo && i.ticket === ticket,
  );
  const live = instance !== undefined;
  const working = instance?.session_state === "working";
  const takenOverHere = instance?.session_state === "takeover";
  const takenOver = instData ? repoTakenOver(instData.instances, repo) : false;
  const root = instData ? repoRoot(instData.repos, repo) : "";
  const session = checkpoint?.data.SESSION ?? "";
  const phase = (working ? instance.phase : "") || run?.phase || "";
  const spawnFailure = feed.events.find(
    (ev) => ev.kind === "spawn_failed" && fieldStr(ev, "ticket") === ticket,
  );
  const variant = deriveVariant({
    phase,
    failureClass: run?.failure_class,
    working,
    live,
    hasCheckpoint: run !== undefined,
    spawnFailed: spawnFailure !== undefined,
  });
  const activity = working ? instance.activity : undefined;
  const detail = working ? instance.detail : undefined;
  const sync = syncState(activity, detail);
  const pill = takenOverHere
    ? sessionStatePill("takeover")
    : headerPill(
        variant,
        phase,
        run?.failure_class,
        activity,
        detail,
        run?.release,
      );
  const { steps, subLabel } = runSteps(variant, phase, activity, detail);

  usePageTitle(runTitle(ticket, pill.label));

  const latestPhaseEventId = useMemo(
    () => feed.events.find((ev) => PHASE_EVENT_KINDS.has(ev.kind))?.id,
    [feed.events],
  );
  useEffect(() => {
    if (!latestPhaseEventId) return;
    void queryClient.invalidateQueries({ queryKey: ["instances"] });
    void queryClient.invalidateQueries({ queryKey: ["run", repo, ticket] });
    void queryClient.invalidateQueries({ queryKey: ["run-diff", repo, ticket] });
    void queryClient.invalidateQueries({
      queryKey: ["phase-logs", repo, ticket],
    });
  }, [latestPhaseEventId, queryClient, repo, ticket]);

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ["instances"] });
    void queryClient.invalidateQueries({ queryKey: ["repos"] });
    void queryClient.invalidateQueries({ queryKey: ["runs", repo] });
    void queryClient.invalidateQueries({ queryKey: ["run", repo, ticket] });
  };

  const stop = useMutation({
    mutationFn: () => stopInstance(instance!.pid),
    onSuccess: invalidate,
  });
  const takeover = useMutation({
    mutationFn: () => takeoverRun(repo, ticket),
    onSuccess: () => {
      toast(
        "Opened in terminal — this ticket stays parked; use Run next to hand it back.",
      );
      invalidate();
    },
    onError: (err) => {
      if (err instanceof TakeoverError && err.status === 501) {
        setTakeoverUnsupported(true);
        return;
      }
      toast.error(err instanceof Error ? err.message : String(err));
    },
  });

  const fixable = proposeFixable(run?.failure_class, live);
  const proposeFix = useProposeFix({
    repo,
    ticket,
    enabled: fixable,
    onStarted: () =>
      void navigate({ to: "/inbox", search: { issue: ticket, repo } }),
  });

  const elapsedMs = deriveElapsedMs(feed.events, ticket);
  const recapElapsed = elapsedMs !== null ? formatDuration(elapsedMs) : null;
  // A live takeover is the header's state, so the stored recap stays out of the
  // page body rather than contradicting it.
  const isRecap =
    !takenOverHere && (variant === "success" || variant === "failure");
  const noSkills = feed.events.some(
    (ev) => ev.kind === "build_no_skills" && fieldStr(ev, "ticket") === ticket,
  );
  const noBrowser = feed.events.some(
    (ev) => ev.kind === "verify_no_browser" && fieldStr(ev, "ticket") === ticket,
  );
  const syncLogs = (phaseLogs?.logs ?? [])
    .filter((log) => isSyncLog(log.phase))
    .sort((a, b) => a.phase.localeCompare(b.phase, undefined, { numeric: true }));

  const openPR =
    run && run.pr && run.pr_url ? (
      <Button asChild size="sm" className="font-mono">
        <a href={run.pr_url} target="_blank" rel="noreferrer">
          <ExternalLink className="size-4" aria-hidden="true" />
          Open PR
        </a>
      </Button>
    ) : null;
  const prBadge = <PRStatusBadge status={run?.pr_status} />;
  const proposeFixAction = fixable ? (
    proposeFix.session ? (
      <Button asChild variant="outline" size="sm" className="font-mono">
        <Link to="/inbox" search={{ issue: ticket, repo }}>
          <Wrench className="size-4" aria-hidden="true" />
          Open fix session
        </Link>
      </Button>
    ) : (
      <>
        <Button
          variant="outline"
          size="sm"
          className="font-mono"
          disabled={proposeFix.starting}
          onClick={proposeFix.start}
        >
          {proposeFix.starting ? (
            <Loader2 className="size-4 animate-spin" aria-hidden="true" />
          ) : (
            <Wrench className="size-4" aria-hidden="true" />
          )}
          {proposeFix.starting ? "Starting…" : "Propose fix"}
        </Button>
        {proposeFix.error ? (
          <span className="font-sans text-xs text-fail">
            {proposeFix.error}
          </span>
        ) : null}
      </>
    )
  ) : null;
  const viewLog = (
    <Button asChild variant="outline" size="sm" className="font-mono">
      <Link to="/runs/$repo/$ticket" params={{ repo, ticket }}>
        <ScrollText className="size-4" aria-hidden="true" />
        View log
      </Link>
    </Button>
  );
  const recapActions =
    variant === "success" ? (
      <>
        {openPR}
        {prBadge}
        {viewLog}
      </>
    ) : (
      <>
        {prBadge}
        {viewLog}
      </>
    );

  return (
    <>
      <RunPageHeader
        className="border-b border-border px-6 py-3"
        ticket={ticket}
        title={run?.title}
        badges={
          <>
            <StatusPill state={pill.state} label={pill.label} />
            {!isRecap && prBadge}
            <span className="rounded-md border border-border bg-secondary/50 px-2 py-0.5 font-mono text-xs text-muted-foreground">
              {repo}
            </span>
          </>
        }
        actions={
          <>
            {proposeFixAction}
            {root !== "" && <OpenInEditor root={root} />}
            {instData?.takeover_supported && !takeoverUnsupported && (
              <Button
                variant="outline"
                size="sm"
                className="font-mono"
                disabled={takenOver || session === "" || takeover.isPending}
                title={
                  takenOver
                    ? "Taken over in a terminal"
                    : session === ""
                      ? "No resumable Claude session"
                      : undefined
                }
                onClick={() => takeover.mutate()}
              >
                {takeover.isPending && working ? (
                  <Loader2 className="size-4 animate-spin" aria-hidden="true" />
                ) : (
                  <SquareTerminal className="size-4" aria-hidden="true" />
                )}
                {takeover.isPending && working
                  ? "Stopping…"
                  : "Open in terminal"}
              </Button>
            )}
            {instance && !takenOverHere && (
              <Button
                variant="destructive"
                size="sm"
                className="font-mono"
                onClick={() => setStopOpen(true)}
              >
                <Square className="size-4" aria-hidden="true" />
                Stop
              </Button>
            )}
          </>
        }
        meta={
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-xs text-muted-foreground">
            <PhaseStepper steps={steps} subLabel={subLabel} compact />
            {instance && (
              <>
                <span className="text-faint" aria-hidden="true">
                  ·
                </span>
                <span>
                  elapsed{" "}
                  <span className="text-foreground">
                    {elapsedSince(instance.started_at, now)}
                  </span>
                </span>
                {instance.state_since && (
                  <>
                    <span className="text-faint" aria-hidden="true">
                      ·
                    </span>
                    <span>
                      in phase{" "}
                      <span className="text-foreground">
                        {elapsedSince(instance.state_since, now)}
                      </span>
                    </span>
                  </>
                )}
              </>
            )}
          </div>
        }
      />

      <div className="flex flex-col gap-4 px-6 py-4">
        {variant === "paused" && !takenOverHere && (
          <PausedBanner reason={run?.failure_reason ?? ""} />
        )}

        {variant === "stopped" && !takenOverHere && <StoppedBanner />}

        {sync && <SyncStateBanner sync={sync} />}

        {noSkills && <NoSkillsBanner />}

        {noBrowser && <NoBrowserBanner />}

        {stop.error && (
          <p className="font-mono text-sm text-destructive">
            {(stop.error as Error).message}
          </p>
        )}

        {syncLogs.length > 0 && <SyncLogs logs={syncLogs} />}

        {isRecap && run ? (
          <div className="grid grid-cols-1 gap-6 lg:grid-cols-5">
            <div className="flex flex-col gap-2 lg:col-span-2">
              <Eyebrow glyph="done">RECAP</Eyebrow>
              <Recap
                run={run}
                variant={variant}
                elapsed={recapElapsed}
                actions={recapActions}
              />
            </div>

            <div className="flex flex-col gap-2 lg:col-span-3">
              <MainPane
                repo={repo}
                ticket={ticket}
                glyph="partial"
                terminal={<Terminal repo={repo} live={live} />}
                sync={sync}
              />
              <SteerComposer
                repo={repo}
                ticket={ticket}
                settled={isRecap}
                className="mt-2"
              />
            </div>
          </div>
        ) : (
          <div className="flex flex-col gap-6">
            {variant === "failed_to_start" ? (
              <FailedToStartBanner error={fieldStr(spawnFailure!, "error")} />
            ) : variant === "starting" ? (
              <StartingPlaceholder />
            ) : (
              <MainPane
                repo={repo}
                ticket={ticket}
                glyph="active"
                terminal={
                  <Terminal
                    repo={repo}
                    since={instance?.started_at}
                    live={live}
                    tall
                  />
                }
                sync={sync}
              />
            )}

            <SteerComposer repo={repo} ticket={ticket} settled={isRecap} />

            <ActivityFeed events={feed.events} />
          </div>
        )}
      </div>

      <ConfirmDialog
        open={stopOpen}
        onOpenChange={setStopOpen}
        windowTitle="confirm"
        title={`Stop run ${ticket}?`}
        description="The run stops now. Work in progress is saved at the last checkpoint and the ticket stays resumable — Start picks it up from there."
        confirmLabel="Stop run"
        destructive
        onConfirm={() => stop.mutate()}
      />
    </>
  );
}
