import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  ExternalLink,
  Play,
  RotateCcw,
  ScrollText,
  Square,
} from "lucide-react";

import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/trau/confirm-dialog";
import { ForceResetDialog } from "@/components/trau/force-reset-dialog";
import { Eyebrow } from "@/components/trau/eyebrow";
import { PhaseStepper } from "@/components/trau/phase-stepper";
import { StatusPill } from "@/components/trau/status-pill";
import { TerminalCard } from "@/components/trau/terminal-card";
import { Terminal } from "@/components/terminal";
import { cn } from "@/lib/utils";
import { useEventFeed, type FeedEvent } from "@/lib/events";
import { CheckpointError, resetRun } from "@/lib/checkpoints";
import {
  instancesQueryOptions,
  startInstance,
  stopInstance,
  type Instance,
} from "@/lib/instances";
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
  runPhaseSteps,
  sumCosts,
  type RunVariant,
} from "@/lib/runlive";

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
  const rows = events.slice(0, 40);
  return (
    <TerminalCard title="activity" bodyClassName="p-0">
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
                <span className="text-pretty text-foreground">{row.text}</span>
              </li>
            );
          })}
        </ul>
      )}
    </TerminalCard>
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
  gated,
}: {
  reason: string;
  onResume: () => void;
  resuming: boolean;
  gated: boolean;
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
          disabled={resuming || gated}
          onClick={onResume}
        >
          <Play className="size-4" aria-hidden="true" />
          {resuming ? "Resuming…" : "Resume"}
        </Button>
      </div>
      {gated && (
        <p className="mt-1 font-mono text-[0.65rem] text-muted-foreground">
          trau is parked on this ticket’s recap in the TUI — handle it there, or
          stop it above to resume from here
        </p>
      )}
    </div>
  );
}

export function RunView({ repo, ticket }: { repo: string; ticket: string }) {
  const queryClient = useQueryClient();
  const now = useNow(1000);
  const [stopOpen, setStopOpen] = useState(false);
  const [resetOpen, setResetOpen] = useState(false);

  const { data: instData } = useQuery(instancesQueryOptions);
  const { data: run } = useQuery(runDetailQueryOptions(repo, ticket));
  const feed = useEventFeed(repo);

  const instance: Instance | undefined = instData?.instances.find(
    (i) => i.repo === repo && i.ticket === ticket,
  );
  const live = instance !== undefined;
  const working = instance?.session_state === "working";
  const parkedHere = instance?.session_state === "parked";
  const phase = (working ? instance.phase : "") || run?.phase || "";
  const variant = deriveVariant({
    phase,
    failureClass: run?.failure_class,
    working,
  });
  const pill = headerPill(variant, phase, run?.failure_class);
  const steps = runPhaseSteps(phase, variant);

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
    mutationFn: () => startInstance({ repo, ticket }),
    onSuccess: invalidate,
  });
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

  const elapsedMs = deriveElapsedMs(feed.events, ticket);
  const recapElapsed = elapsedMs !== null ? formatDuration(elapsedMs) : null;
  const isRecap = variant === "success" || variant === "failure";

  const openPR =
    run && run.pr && run.pr_url ? (
      <Button asChild size="sm" className="font-mono">
        <a href={run.pr_url} target="_blank" rel="noreferrer">
          <ExternalLink className="size-4" aria-hidden="true" />
          Open PR
        </a>
      </Button>
    ) : null;
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
      disabled={resume.isPending || parkedHere}
      onClick={() => resume.mutate()}
    >
      <Play className="size-4" aria-hidden="true" />
      {resume.isPending ? "Resuming…" : "Resume"}
    </Button>
  );
  const parkedGate = parkedHere ? (
    <p className="w-full font-mono text-[0.65rem] text-muted-foreground">
      trau is parked on this ticket’s recap in the TUI — handle it there, or
      stop it above to resume from here
    </p>
  ) : null;
  const forceResetBtn = (
    <Button
      variant="ghost"
      size="sm"
      className="font-mono"
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
          disabled={reset.isPending}
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
        {viewLog}
        {forceResetBtn}
      </>
    ) : (
      <>
        {viewLog}
        {resumeBtn}
        {plainResetBtn}
        {parkedGate}
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
              <span className="rounded-md border border-border bg-secondary/50 px-2 py-0.5 font-mono text-xs text-muted-foreground">
                {repo}
              </span>
            </div>
          </div>

          {instance && (
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

        <div className="rounded-md border border-border bg-secondary/30 px-4 py-3">
          <PhaseStepper steps={steps} />
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
        {variant === "paused" && (
          <PausedBanner
            reason={run?.failure_reason ?? ""}
            onResume={() => resume.mutate()}
            resuming={resume.isPending}
            gated={parkedHere}
          />
        )}

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
              {(reset.error as Error).message}
            </p>
          )}
        {stop.error && (
          <p className="font-mono text-sm text-destructive">
            {(stop.error as Error).message}
          </p>
        )}

        <div className="grid grid-cols-1 gap-6 lg:grid-cols-5">
          <div className="flex flex-col gap-2 lg:col-span-2">
            <Eyebrow glyph={isRecap ? "done" : "active"}>
              {isRecap ? "RECAP" : "ACTIVITY"}
            </Eyebrow>
            {isRecap && run ? (
              <Recap
                run={run}
                variant={variant}
                elapsed={recapElapsed}
                actions={recapActions}
              />
            ) : (
              <ActivityFeed events={feed.events} />
            )}
          </div>

          <div className="flex flex-col gap-2 lg:col-span-3">
            <Eyebrow glyph="partial">TRANSCRIPT</Eyebrow>
            <Terminal repo={repo} live={live} />
          </div>
        </div>
      </div>

      <ConfirmDialog
        open={stopOpen}
        onOpenChange={setStopOpen}
        windowTitle="confirm"
        title={`Stop run ${ticket}?`}
        description="The run stops gracefully at the last checkpoint. Work in progress is preserved."
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
    </>
  );
}
