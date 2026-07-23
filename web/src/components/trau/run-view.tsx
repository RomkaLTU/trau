import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import {
  ChevronRight,
  ExternalLink,
  Loader2,
  Play,
  RotateCcw,
  ScrollText,
  Square,
  SquareTerminal,
} from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/trau/confirm-dialog";
import { ForceResetDialog } from "@/components/trau/force-reset-dialog";
import { useHandback } from "@/components/trau/handback-dialog";
import { Eyebrow } from "@/components/trau/eyebrow";
import { NoSkillsBanner } from "@/components/trau/no-skills-banner";
import { NoBrowserBanner } from "@/components/trau/no-browser-banner";
import { PhaseStepper } from "@/components/trau/phase-stepper";
import { PRStatusBadge } from "@/components/trau/pr-status-badge";
import { SteerComposer } from "@/components/trau/steer-notes";
import { StatusPill } from "@/components/trau/status-pill";
import { TerminalCard } from "@/components/trau/terminal-card";
import { Terminal } from "@/components/terminal";
import { cn } from "@/lib/utils";
import { useEventFeed, type FeedEvent } from "@/lib/events";
import { runTitle, usePageTitle } from "@/lib/page-title";
import {
  CheckpointError,
  checkpointErrorText,
  resetRun,
  runCheckpointQueryOptions,
} from "@/lib/checkpoints";
import {
  instancesQueryOptions,
  repoTakenOver,
  stopInstance,
  takeoverRun,
  TAKEOVER_BLOCKED,
  TakeoverError,
  type Instance,
} from "@/lib/instances";
import { sessionStatePill } from "@/lib/overview";
import { publishQueue, runNext } from "@/lib/queue";
import { runDetailQueryOptions, type RunDetail } from "@/lib/rundetail";
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
// per-line agent activity); one arriving forces an immediate registry + run
// refetch so the title snaps. Everything else rides the normal poll.
const PHASE_EVENT_KINDS = new Set([
  "agent_start",
  "activity_change",
  "pr_open",
  "state_change",
]);

const PARKED_GATE =
  "trau is parked on this ticket’s recap in the TUI — handle it there, or stop it above to resume from here";

function useNow(intervalMs: number): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);
  return now;
}

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
          {run.pr && run.pr_url && (
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

function PausedBanner({
  reason,
  onResume,
  resuming,
  gate,
}: {
  reason: string;
  onResume: () => void;
  resuming: boolean;
  gate: string;
}) {
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
      <div className="mt-2">
        <Button
          size="sm"
          className="font-mono"
          disabled={resuming || gate !== ""}
          title={gate || undefined}
          onClick={onResume}
        >
          <Play className="size-4" aria-hidden="true" />
          {resuming ? "Resuming…" : "Resume"}
        </Button>
      </div>
      {gate && (
        <p className="mt-1 font-mono text-[0.65rem] text-muted-foreground">
          {gate}
        </p>
      )}
    </div>
  );
}

function FailedToStartBanner({
  error,
  onRetry,
  retrying,
}: {
  error: string;
  onRetry: () => void;
  retrying: boolean;
}) {
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
      <div className="mt-1">
        <Button
          size="sm"
          className="font-mono"
          disabled={retrying}
          onClick={onRetry}
        >
          <Play className="size-4" aria-hidden="true" />
          {retrying ? "Retrying…" : "Retry"}
        </Button>
      </div>
    </div>
  );
}

function StoppedBanner({
  onResume,
  resuming,
  gate,
}: {
  onResume: () => void;
  resuming: boolean;
  gate: string;
}) {
  return (
    <div className="flex flex-col gap-1 rounded-lg border border-info/40 bg-info/10 px-4 py-3">
      <span className="inline-flex items-center gap-2 font-mono text-sm text-info">
        <span aria-hidden="true">⏹</span>
        {STOPPED_HEADLINE}
      </span>
      <p className="font-sans text-sm leading-relaxed text-muted-foreground">
        {STOPPED_HINT}
      </p>
      <div className="mt-2">
        <Button
          size="sm"
          className="font-mono"
          disabled={resuming || gate !== ""}
          title={gate || undefined}
          onClick={onResume}
        >
          <Play className="size-4" aria-hidden="true" />
          {resuming ? "Resuming…" : "Resume"}
        </Button>
      </div>
      {gate && (
        <p className="mt-1 font-mono text-[0.65rem] text-muted-foreground">
          {gate}
        </p>
      )}
    </div>
  );
}

function GateNote({ text }: { text: string }) {
  return (
    <p className="w-full font-mono text-[0.65rem] text-muted-foreground">
      {text}
    </p>
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
  const [resetOpen, setResetOpen] = useState(false);
  const [takeoverUnsupported, setTakeoverUnsupported] = useState(false);

  const { data: instData } = useQuery(instancesQueryOptions);
  const { data: run } = useQuery(runDetailQueryOptions(repo, ticket));
  const { data: checkpoint } = useQuery(
    runCheckpointQueryOptions(repo, ticket),
  );
  const feed = useEventFeed(repo);

  const instance: Instance | undefined = instData?.instances.find(
    (i) => i.repo === repo && i.ticket === ticket,
  );
  const live = instance !== undefined;
  const working = instance?.session_state === "working";
  const parkedHere = instance?.session_state === "parked";
  const takenOverHere = instance?.session_state === "takeover";
  const takenOver = instData ? repoTakenOver(instData.instances, repo) : false;
  // Resume hands the ticket back to the loop, which cannot have the repo while a
  // terminal holds it or while the TUI is parked on this ticket's recap.
  const resumeGate = takenOver
    ? TAKEOVER_BLOCKED
    : parkedHere
      ? PARKED_GATE
      : "";
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
  const pill = takenOverHere
    ? sessionStatePill("takeover")
    : headerPill(variant, phase, run?.failure_class, activity);
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
  const resume = useMutation({
    mutationFn: () => runNext(repo, { id: ticket }),
    onSuccess: (res) => {
      publishQueue(queryClient, repo, res);
      void navigate({ to: "/loop" });
    },
  });
  const handback = useHandback(repo, () => resume.mutate());
  const startResume = () => handback.request(ticket, run?.handback ?? null);
  const reset = useMutation({
    mutationFn: (force: boolean) => resetRun(repo, ticket, force),
    onSuccess: () => {
      setResetOpen(false);
      invalidate();
    },
    onError: (err) => {
      if (err instanceof CheckpointError && err.requiresForce)
        setResetOpen(true);
    },
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
  const viewLog = (
    <Button asChild variant="outline" size="sm" className="font-mono">
      <Link to="/runs/$repo/$ticket" params={{ repo, ticket }}>
        <ScrollText className="size-4" aria-hidden="true" />
        View log
      </Link>
    </Button>
  );
  const resumeBtn = (
    <Button
      variant="outline"
      size="sm"
      className="font-mono"
      disabled={resume.isPending || resumeGate !== ""}
      title={resumeGate || undefined}
      onClick={startResume}
    >
      <Play className="size-4" aria-hidden="true" />
      {resume.isPending ? "Resuming…" : "Resume"}
    </Button>
  );
  const resumeGateNote = resumeGate ? <GateNote text={resumeGate} /> : null;
  const forceResetBtn = (
    <Button
      variant="ghost"
      size="sm"
      className="font-mono"
      disabled={takenOver}
      title={takenOver ? TAKEOVER_BLOCKED : undefined}
      onClick={() => setResetOpen(true)}
    >
      <RotateCcw className="size-4" aria-hidden="true" />
      Reset
    </Button>
  );
  const plainResetBtn = (
    <ConfirmDialog
      windowTitle="confirm"
      trigger={
        <Button
          variant="ghost"
          size="sm"
          className="font-mono"
          disabled={reset.isPending || takenOver}
          title={takenOver ? TAKEOVER_BLOCKED : undefined}
        >
          <RotateCcw className="size-4" aria-hidden="true" />
          {reset.isPending ? "Resetting…" : "Reset"}
        </Button>
      }
      title={`Reset ${ticket}?`}
      description={`Drops ${ticket}'s branch and checkpoint and re-queues it on the tracker.`}
      confirmLabel="Reset"
      onConfirm={() => reset.mutate(false)}
    />
  );

  const recapActions =
    variant === "success" ? (
      <>
        {openPR}
        {prBadge}
        {viewLog}
        {forceResetBtn}
        {takenOver && <GateNote text={TAKEOVER_BLOCKED} />}
      </>
    ) : (
      <>
        {prBadge}
        {viewLog}
        {resumeBtn}
        {plainResetBtn}
        {resumeGateNote}
      </>
    );

  return (
    <>
      <header className="flex flex-col gap-4 border-b border-border px-8 py-6">
        <div className="flex flex-wrap items-center justify-between gap-4">
          <div className="flex flex-col gap-2">
            <Eyebrow
              glyph={isRecap ? "done" : "active"}
              className={isRecap ? undefined : "text-teal"}
            >
              RUN
            </Eyebrow>
            <div className="flex flex-wrap items-center gap-3">
              <span className="font-mono text-sm text-primary">{ticket}</span>
              {run?.title && (
                <h1 className="text-balance font-sans text-2xl font-semibold tracking-tight text-foreground">
                  {run.title}
                </h1>
              )}
              <StatusPill state={pill.state} label={pill.label} />
              {!isRecap && prBadge}
              <span className="rounded-md border border-border bg-secondary/50 px-2 py-0.5 font-mono text-xs text-muted-foreground">
                {repo}
              </span>
            </div>
          </div>

          <div className="flex items-center gap-2">
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
          </div>
        </div>

        <div className="rounded-md border border-border bg-secondary/30 px-4 py-3">
          <PhaseStepper steps={steps} subLabel={subLabel} />
        </div>

        {instance && (
          <div className="flex items-center gap-6 font-mono text-xs text-muted-foreground">
            <span>
              elapsed{" "}
              <span className="text-foreground">
                {elapsedSince(instance.started_at, now)}
              </span>
            </span>
            {instance.state_since && (
              <span>
                in phase{" "}
                <span className="text-foreground">
                  {elapsedSince(instance.state_since, now)}
                </span>
              </span>
            )}
          </div>
        )}
      </header>

      <div className="flex flex-col gap-6 p-8">
        {variant === "paused" && !takenOverHere && (
          <PausedBanner
            reason={run?.failure_reason ?? ""}
            onResume={startResume}
            resuming={resume.isPending}
            gate={resumeGate}
          />
        )}

        {variant === "stopped" && !takenOverHere && (
          <StoppedBanner
            onResume={startResume}
            resuming={resume.isPending}
            gate={resumeGate}
          />
        )}

        {noSkills && <NoSkillsBanner />}

        {noBrowser && <NoBrowserBanner />}

        {resume.error && (
          <p className="font-mono text-sm text-destructive">
            {(resume.error as Error).message}
          </p>
        )}
        {reset.error &&
          !(
            reset.error instanceof CheckpointError && reset.error.requiresForce
          ) && (
            <p className="font-mono text-sm text-destructive">
              {checkpointErrorText(reset.error)}
            </p>
          )}
        {stop.error && (
          <p className="font-mono text-sm text-destructive">
            {(stop.error as Error).message}
          </p>
        )}

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
              <Eyebrow glyph="partial">TRANSCRIPT</Eyebrow>
              <Terminal repo={repo} live={live} />
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
              <FailedToStartBanner
                error={fieldStr(spawnFailure!, "error")}
                onRetry={startResume}
                retrying={resume.isPending}
              />
            ) : variant === "starting" ? (
              <StartingPlaceholder />
            ) : (
              <div className="flex flex-col gap-2">
                <Eyebrow glyph="active">TRANSCRIPT</Eyebrow>
                <Terminal
                  repo={repo}
                  since={instance?.started_at}
                  live={live}
                  tall
                />
              </div>
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
        description="The run stops now. Work in progress is saved at the last checkpoint and the ticket stays resumable."
        confirmLabel="Stop run"
        destructive
        onConfirm={() => stop.mutate()}
      />
      <ForceResetDialog
        open={resetOpen}
        onOpenChange={setResetOpen}
        ticket={ticket}
        pending={reset.isPending}
        onConfirm={() => reset.mutate(true)}
      />
      {handback.dialog}
    </>
  );
}
