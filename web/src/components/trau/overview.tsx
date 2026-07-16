import { useEffect, useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { Eye, RefreshCw, Square } from "lucide-react";

import { Button } from "@/components/ui/button";
import { useActiveRepo } from "@/components/trau/active-repo";
import { EmptyState } from "@/components/trau/empty-state";
import { Eyebrow } from "@/components/trau/eyebrow";
import { PhaseStepper } from "@/components/trau/phase-stepper";
import { StatusPill, type RunState } from "@/components/trau/status-pill";
import { TerminalCard } from "@/components/trau/terminal-card";
import { cn } from "@/lib/utils";
import { useAttentionRuns } from "@/lib/attention";
import { costsQueryOptions } from "@/lib/costs";
import { stopInstance } from "@/lib/instances";
import {
  activeLoopCount,
  attentionPill,
  loopCardView,
  phasePill,
  recentRuns,
  useLiveLoops,
  useRepoActivity,
  type LiveLoop,
} from "@/lib/overview";
import { publishQueue, runNext } from "@/lib/queue";
import { runsQueryOptions, type FailureClass, type Run } from "@/lib/runs";
import { liveSteps } from "@/lib/steps";

export { PhaseStepper };

export function useNow(intervalMs: number): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);
  return now;
}

export function elapsed(fromISO: string, now: number): string {
  const s = Math.max(0, Math.floor((now - new Date(fromISO).getTime()) / 1000));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const rem = s % 60;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${rem}s`;
  return `${rem}s`;
}

// ago renders a compact "time since" for finished runs — days collapse to a
// single unit, everything under a day keeps hours + zero-padded minutes.
function ago(fromISO: string | undefined, now: number): string {
  if (!fromISO) return "";
  const s = Math.max(0, Math.floor((now - new Date(fromISO).getTime()) / 1000));
  const d = Math.floor(s / 86400);
  if (d > 0) return `${d}d`;
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (h > 0) return `${h}h ${String(m).padStart(2, "0")}m`;
  if (m > 0) return `${m}m`;
  return `${s}s`;
}

/* ---------- pulse strip ---------- */

export function PulseStrip() {
  const { repo, isAll } = useActiveRepo();
  const activity = useRepoActivity();
  const { data: costs } = useQuery(costsQueryOptions(1));

  const scoped = isAll ? activity : activity.filter((a) => a.repo.name === repo);
  const running = scoped.reduce((n, a) => n + activeLoopCount(a.loops), 0);
  const attention = scoped.reduce((n, a) => n + a.attention.length, 0);
  const idle = scoped.filter((a) => activeLoopCount(a.loops) === 0).length;
  const spend = scoped.reduce((s, a) => s + a.spend, 0);
  const metered = scoped.every((a) => a.metered);

  const repoBudget = isAll
    ? undefined
    : costs?.repos.find((c) => c.repo === repo)?.daily_budget_usd;
  const budget = repoBudget ?? costs?.budget.daily_usd;
  const spendPct = budget ? Math.min(100, (spend / budget) * 100) : 0;

  return (
    <div className="flex flex-col gap-3 rounded-lg border border-border bg-card px-4 py-3 sm:flex-row sm:items-center sm:gap-6">
      <div className="flex items-center gap-6">
        <div className="flex items-center gap-2 font-mono text-sm">
          <span aria-hidden="true" className="text-teal">
            ●
          </span>
          <span className="text-foreground">{running}</span>
          <span className="text-muted-foreground">running</span>
        </div>
        <div className="flex items-center gap-2 font-mono text-sm">
          <span
            aria-hidden="true"
            className={attention > 0 ? "text-warn" : "text-faint"}
          >
            ⚠
          </span>
          <span className={attention > 0 ? "text-warn" : "text-foreground"}>
            {attention}
          </span>
          <span className="text-muted-foreground">need you</span>
        </div>
        <div className="flex items-center gap-2 font-mono text-sm">
          <span aria-hidden="true" className="text-faint">
            ○
          </span>
          <span className="text-foreground">{idle}</span>
          <span className="text-muted-foreground">idle</span>
        </div>
      </div>

      <div className="flex flex-1 items-center gap-3 sm:justify-end">
        <span className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
          spend today
        </span>
        {budget ? (
          <div
            className="h-1 w-24 overflow-hidden rounded-full bg-secondary"
            role="progressbar"
            aria-valuenow={Number(spend.toFixed(2))}
            aria-valuemin={0}
            aria-valuemax={budget}
            aria-label="spend today"
          >
            <div
              className="h-full rounded-full bg-primary"
              style={{ width: `${spendPct}%` }}
            />
          </div>
        ) : null}
        <span className="font-mono text-sm text-foreground">
          {metered ? "" : "≥ "}${spend.toFixed(2)}
          {budget ? (
            <span className="text-muted-foreground"> / ${budget.toFixed(0)}</span>
          ) : null}
        </span>
      </div>
    </div>
  );
}
/* ---------- launch actions (header) ---------- */

export function LaunchActions() {
  return (
    <div className="flex items-center gap-2">
      <Button asChild className="font-mono">
        <Link to="/loop">
          <RefreshCw className="size-4" aria-hidden="true" />
          Start loop
        </Link>
      </Button>
    </div>
  );
}

function MetaItem({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col gap-0.5">
      <span className="font-mono text-[0.6rem] uppercase tracking-[0.18em] text-muted-foreground">
        {label}
      </span>
      <span className="font-mono text-sm text-foreground">{value}</span>
    </div>
  );
}

export function MetaInline({ label, value }: { label: string; value: string }) {
  return (
    <span className="flex items-baseline gap-1.5">
      <span className="font-mono text-[0.6rem] uppercase tracking-[0.18em] text-muted-foreground">
        {label}
      </span>
      <span className="font-mono text-xs text-foreground">{value}</span>
    </span>
  );
}

function actionError(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export function StopButton({
  pid,
  repo,
  disabled = false,
}: {
  pid: number;
  repo: string;
  disabled?: boolean;
}) {
  const queryClient = useQueryClient();
  const stop = useMutation({
    mutationFn: () => stopInstance(pid),
    onSuccess: () =>
      void queryClient.invalidateQueries({ queryKey: ["instances"] }),
  });
  return (
    <div className="flex flex-col items-start gap-1">
      <Button
        variant="ghost"
        size="sm"
        className="font-mono text-fail hover:text-fail"
        disabled={disabled || stop.isPending}
        onClick={() => stop.mutate()}
        title={`Stop the loop in ${repo}`}
      >
        <Square className="size-4" aria-hidden="true" />
        {stop.isPending ? "Stopping…" : "Stop"}
      </Button>
      {stop.error && (
        <p className="text-xs text-destructive">
          Couldn’t stop {repo}: {actionError(stop.error)}
        </p>
      )}
    </div>
  );
}

/* ---------- single-repo focus: live loop card ---------- */

function LoopCard({ loop, now }: { loop: LiveLoop; now: number }) {
  const view = loopCardView(loop.sessionState, {
    phase: loop.phase,
    activity: loop.activity,
    detail: loop.detail,
    failureClass: loop.failureClass,
  });
  return (
    <TerminalCard title={loop.repo} scanlines>
      <div className={cn("flex flex-col gap-4", view.dimmed && "opacity-60")}>
        {loop.ticket ? (
          <div className="flex flex-col gap-1">
            <div className="flex items-center gap-2">
              <Link
                to="/runs/$repo/$ticket"
                params={{ repo: loop.repo, ticket: loop.ticket }}
                className="font-mono text-sm text-primary hover:underline"
              >
                {loop.ticket}
              </Link>
              <StatusPill state={view.pill.state} label={view.pill.label} />
            </div>
            {loop.title && (
              <p className="text-pretty font-sans text-sm leading-relaxed text-foreground">
                {loop.title}
              </p>
            )}
          </div>
        ) : (
          <div className="flex items-center gap-2">
            <StatusPill state={view.pill.state} label={view.pill.label} />
            {view.copy && (
              <span className="font-sans text-sm text-muted-foreground">
                {view.copy}
              </span>
            )}
          </div>
        )}

        {loop.ticket && view.copy && (
          <div className="flex flex-col gap-1">
            {loop.failureReason && (
              <p className="text-pretty font-sans text-sm leading-relaxed text-foreground">
                {loop.failureReason}
              </p>
            )}
            <p className="font-sans text-sm text-muted-foreground">
              {view.copy}
            </p>
          </div>
        )}

        <div className="flex items-center gap-6">
          <MetaItem label="elapsed" value={elapsed(loop.startedAt, now)} />
          {view.showStepper && loop.stateSince && (
            <MetaItem label="in phase" value={elapsed(loop.stateSince, now)} />
          )}
        </div>

        {view.showStepper && (
          <div className="rounded-md border border-border bg-secondary/30 px-3 py-2.5">
            <PhaseStepper {...liveSteps(loop.activity, loop.detail, loop.phase)} />
          </div>
        )}

        <div className="flex items-start gap-2">
          {view.showWatch && loop.ticket && (
            <Button asChild variant="outline" size="sm" className="font-mono">
              <Link
                to="/live/$repo/$ticket"
                params={{ repo: loop.repo, ticket: loop.ticket }}
              >
                <Eye className="size-4" aria-hidden="true" />
                Watch
              </Link>
            </Button>
          )}
          {view.showStop && (
            <StopButton
              pid={loop.pid}
              repo={loop.repo}
              disabled={view.stopDisabled}
            />
          )}
        </div>
      </div>
    </TerminalCard>
  );
}

function LiveLoops() {
  const { repo } = useActiveRepo();
  const loops = useLiveLoops(repo);
  const now = useNow(1000);

  if (loops.length === 0) {
    return (
      <EmptyState
        message="No loops running right now. Point trau at a ticket to watch it work."
        actions={
          <Button asChild size="sm" className="font-mono">
            <Link to="/loop">
              <RefreshCw className="size-4" aria-hidden="true" />
              Start loop
            </Link>
          </Button>
        }
      />
    );
  }

  return (
    <div className="flex flex-col gap-4">
      {loops.map((loop) => (
        <LoopCard key={loop.pid} loop={loop} now={now} />
      ))}
    </div>
  );
}

/* ---------- single-repo focus: needs attention ---------- */

export const ATTENTION_META: Record<
  FailureClass,
  { action: string; resume: boolean }
> = {
  // paused/faulted resume from the checkpoint (start a run); quarantined keeps the
  // Reset navigation to the live view's action menu, where the destructive reset
  // stays behind its own confirm.
  paused: { action: "Resume", resume: true },
  faulted: { action: "Resume", resume: true },
  gave_up: { action: "Reset", resume: false },
};

function liveGateMessage(loop: LiveLoop | undefined): string {
  if (!loop) return "a loop is live in this repo — stop it before resuming";
  const label = loop.sessionState === "unknown" ? "live" : loop.sessionState;
  return `a loop is ${label}${loop.ticket ? ` ${loop.ticket}` : ""} in this repo…`;
}

function NeedsAttention() {
  const { repo, repos } = useActiveRepo();
  const attention = useAttentionRuns(repo);
  const loops = useLiveLoops(repo);
  const navigate = useNavigate();
  const queryClient = useQueryClient();

  // A loop already holding this repo's working tree makes a resume unsafe — the
  // server refuses it with a 409, so the client disables the action to match. A
  // single working tree means at most one loop blocks resume here.
  const isLive = repos.find((r) => r.name === repo)?.live ?? false;
  const liveLoop = loops[0];

  const resume = useMutation({
    mutationFn: (ticket: string) => runNext(repo ?? "", { id: ticket }),
    onSuccess: (res) => {
      publishQueue(queryClient, repo ?? "", res);
      void navigate({ to: "/loop" });
    },
  });

  const stopLive = useMutation({
    mutationFn: (pid: number) => stopInstance(pid),
    onSuccess: () =>
      void queryClient.invalidateQueries({ queryKey: ["instances"] }),
  });

  if (attention.length === 0) {
    return (
      <TerminalCard title="needs-attention">
        <p className="font-sans text-sm text-muted-foreground">
          Nothing waiting on you — every loop is healthy.
        </p>
      </TerminalCard>
    );
  }

  return (
    <TerminalCard title="needs-attention" bodyClassName="p-0">
      <ul className="flex flex-col">
        {attention.map((run) => {
          const meta = ATTENTION_META[run.failure_class!];
          const pill = attentionPill(run.failure_class!);
          const pending = resume.isPending && resume.variables === run.ticket;
          const failed = resume.isError && resume.variables === run.ticket;
          const parkedHere =
            isLive &&
            liveLoop?.sessionState === "parked" &&
            liveLoop.ticket === run.ticket;
          const stopping =
            stopLive.isPending && stopLive.variables === liveLoop?.pid;
          const stopFailed =
            stopLive.isError && stopLive.variables === liveLoop?.pid;
          return (
            <li
              key={`${run.repo} ${run.ticket}`}
              className="flex flex-col gap-2 border-b border-border/60 px-4 py-3 last:border-0"
            >
              <div className="flex items-center gap-2">
                <StatusPill state={pill.state} label={pill.label} />
                <span className="font-mono text-xs text-primary">
                  {run.ticket}
                </span>
              </div>
              <p className="text-pretty font-sans text-sm leading-relaxed text-muted-foreground">
                {run.failure_reason || run.title || run.repo}
              </p>
              {meta.resume ? (
                <>
                  <Button
                    variant="link"
                    size="sm"
                    className="h-auto w-fit p-0 font-mono text-xs text-teal"
                    disabled={isLive || pending}
                    onClick={() => resume.mutate(run.ticket)}
                  >
                    {pending ? "Resuming…" : `${meta.action} →`}
                  </Button>
                  {isLive && parkedHere ? (
                    <>
                      <span className="font-mono text-[0.65rem] text-muted-foreground">
                        trau is parked on this ticket’s recap in the TUI —
                        handle it there, or stop it to resume from here
                      </span>
                      <div className="flex flex-col items-start gap-1">
                        <Button
                          variant="ghost"
                          size="sm"
                          className="h-auto w-fit gap-1 p-0 font-mono text-xs text-fail hover:text-fail"
                          disabled={stopping}
                          onClick={() => stopLive.mutate(liveLoop!.pid)}
                        >
                          <Square className="size-3.5" aria-hidden="true" />
                          {stopping ? "Stopping…" : "Stop"}
                        </Button>
                        {stopFailed ? (
                          <span className="font-mono text-[0.65rem] text-destructive">
                            {stopLive.error instanceof Error
                              ? stopLive.error.message
                              : "stop failed"}
                          </span>
                        ) : null}
                      </div>
                    </>
                  ) : isLive ? (
                    <span className="font-mono text-[0.65rem] text-muted-foreground">
                      {liveGateMessage(liveLoop)}
                    </span>
                  ) : null}
                  {failed ? (
                    <span className="font-mono text-[0.65rem] text-destructive">
                      {resume.error instanceof Error
                        ? resume.error.message
                        : "resume failed"}
                    </span>
                  ) : null}
                </>
              ) : (
                <Link
                  to="/live/$repo/$ticket"
                  params={{ repo: run.repo, ticket: run.ticket }}
                  className="w-fit font-mono text-xs text-teal underline-offset-4 hover:underline"
                >
                  {meta.action} →
                </Link>
              )}
            </li>
          );
        })}
      </ul>
    </TerminalCard>
  );
}

/* ---------- single-repo focus: recent runs ---------- */

function runPill(run: Run): { state: RunState; label: string } {
  return run.failure_class ? attentionPill(run.failure_class) : phasePill(run.phase);
}

function RecentRunsPanel({ repo }: { repo: string }) {
  const { data } = useQuery(runsQueryOptions(repo));
  const now = useNow(30_000);
  const runs = recentRuns(data?.runs ?? []);

  return (
    <Panel title="recent runs" count={runs.length}>
      {runs.length === 0 ? (
        <p className="px-5 py-6 font-sans text-sm text-muted-foreground">
          No runs recorded for this repo yet.
        </p>
      ) : (
        <ul className="flex flex-col divide-y divide-border/60">
          {runs.map((run) => {
            const pill = runPill(run);
            return (
              <li key={run.ticket}>
                <Link
                  to="/runs/$repo/$ticket"
                  params={{ repo, ticket: run.ticket }}
                  className="flex flex-wrap items-center gap-x-3 gap-y-1.5 px-5 py-3 transition-colors hover:bg-secondary/40"
                >
                  <span className="font-mono text-sm text-primary">
                    {run.ticket}
                  </span>
                  <span className="min-w-0 flex-1 truncate font-sans text-sm text-foreground">
                    {run.title ?? run.ticket}
                  </span>
                  <StatusPill state={pill.state} label={pill.label} />
                  <span className="w-14 text-right font-mono text-[0.7rem] text-muted-foreground">
                    {ago(run.updated_at, now)}
                  </span>
                </Link>
              </li>
            );
          })}
        </ul>
      )}
    </Panel>
  );
}

export function RepoFocus({ repo }: { repo: string }) {
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Eyebrow glyph="active">LIVE LOOPS</Eyebrow>
        <LiveLoops />
      </div>
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <div className="flex flex-col gap-2">
          <Eyebrow glyph="warn">NEEDS ATTENTION</Eyebrow>
          <NeedsAttention />
        </div>
        <div className="flex flex-col gap-2">
          <Eyebrow glyph="action">RECENT RUNS</Eyebrow>
          <RecentRunsPanel repo={repo} />
        </div>
      </div>
    </div>
  );
}

/* ---------- shared overview panel ---------- */

export function Panel({
  title,
  count,
  children,
  bodyClassName,
}: {
  title: string;
  count?: number;
  children: ReactNode;
  bodyClassName?: string;
}) {
  return (
    <section className="overflow-hidden rounded-lg border border-border bg-card">
      <header className="flex items-center gap-3 border-b border-border px-5 py-2.5">
        <div className="flex items-center gap-1.5" aria-hidden="true">
          <span className="size-2.5 rounded-full bg-fail" />
          <span className="size-2.5 rounded-full bg-warn" />
          <span className="size-2.5 rounded-full bg-done" />
        </div>
        <span className="font-mono text-xs text-muted-foreground">{title}</span>
        {typeof count === "number" ? (
          <span className="ml-auto font-mono text-[0.65rem] text-faint">
            {count}
          </span>
        ) : null}
      </header>
      <div className={bodyClassName}>{children}</div>
    </section>
  );
}
