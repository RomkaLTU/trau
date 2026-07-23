import { useQueries, useQuery } from "@tanstack/react-query";

import type { RunState } from "@/components/trau/status-pill";
import { costsQueryOptions } from "./costs";
import type { AttentionRun } from "./attention";
import {
  instancesQueryOptions,
  type Instance,
  type RepoView,
} from "./instances";
import {
  reposQueryOptions,
  runsQueryOptions,
  type FailureClass,
  type Run,
} from "./runs";
import { stepPill } from "./steps";

const PHASE_RANK: Record<string, number> = {
  building: 1,
  built: 2,
  handed_off: 3,
  verified: 4,
  pr_open: 5,
  merged: 6,
};

export function phaseRank(phase: string): number {
  return PHASE_RANK[phase] ?? 0;
}

export function phasePill(phase: string): { state: RunState; label: string } {
  switch (phase) {
    case "building":
    case "built":
      return { state: "active", label: "build" };
    case "handed_off":
      return { state: "active", label: "handoff" };
    case "verified":
      return { state: "verify", label: "verify" };
    case "pr_open":
      return { state: "info", label: "pr" };
    case "merged":
      return { state: "success", label: "merged" };
    default:
      return { state: "active", label: phase || "running" };
  }
}

export function boardPill(run: Pick<Run, "phase" | "failure_class">): {
  state: RunState;
  label: string;
} {
  switch (run.failure_class) {
    case "paused":
      return { state: "warn", label: "paused" };
    case "stopped":
      return { state: "warn", label: "stopped" };
    case "faulted":
      return { state: "fail", label: "fault" };
    case "gave_up":
      return { state: "fail", label: "quarantined" };
    default:
      return phasePill(run.phase);
  }
}

export type SessionState =
  | "working"
  | "grazing"
  | "parked"
  | "idle"
  | "stopping"
  | "takeover"
  | "unknown";

export interface LiveLoop {
  repo: string;
  pid: number;
  ticket?: string;
  title?: string;
  sessionState: SessionState;
  phase: string;
  activity?: string;
  detail?: string;
  startedAt: string;
  stateSince?: string;
  failureClass?: FailureClass;
  failureReason?: string;
}

export function toSessionState(raw: string): SessionState {
  switch (raw) {
    case "working":
    case "grazing":
    case "parked":
    case "idle":
    case "stopping":
    case "takeover":
      return raw;
    default:
      return "unknown";
  }
}

export function makeLiveLoop(inst: Instance, run: Run | undefined): LiveLoop {
  const state = toSessionState(inst.session_state);
  return {
    repo: inst.repo,
    pid: inst.pid,
    ticket: inst.ticket,
    title: run?.title,
    sessionState: state,
    phase: state === "working" ? (inst.phase ?? "") : "",
    activity: state === "working" ? (inst.activity ?? "") : "",
    detail: state === "working" ? (inst.detail ?? "") : "",
    startedAt: inst.started_at,
    stateSince: inst.state_since,
    failureClass: run?.failure_class,
    failureReason: run?.failure_reason,
  };
}

export function useLiveLoops(repo: string | null): LiveLoop[] {
  const { data } = useQuery(instancesQueryOptions);
  const instances = (data?.instances ?? []).filter((i) => i.repo === repo);

  const { data: runs } = useQuery(runsQueryOptions(repo ?? ""));
  const byTicket = new Map<string, Run>();
  for (const run of runs?.runs ?? []) {
    byTicket.set(run.ticket, run);
  }

  return instances.map((inst) =>
    makeLiveLoop(inst, inst.ticket ? byTicket.get(inst.ticket) : undefined),
  );
}

// useRunsByRepo fans out the per-repo runs endpoint across every repo so the
// multi-repo board can join loop titles and surface attention/recent runs in
// one place. React Query dedupes each key, so screens already reading a single
// repo's runs share these fetches.
export function useRunsByRepo(names: string[]): Map<string, Run[]> {
  const results = useQueries({
    queries: names.map((name) => runsQueryOptions(name)),
  });
  const byRepo = new Map<string, Run[]>();
  names.forEach((name, i) => {
    byRepo.set(name, results[i]?.data?.runs ?? []);
  });
  return byRepo;
}

export interface RepoActivity {
  repo: RepoView;
  loops: LiveLoop[];
  attention: AttentionRun[];
  spend: number;
  metered: boolean;
}

// useRepoActivity aggregates the global instances/costs feeds plus per-repo runs
// into one row per registered repo — the data behind the Overview board and the
// pulse strip's running/needs-you/idle/spend tallies.
export function useRepoActivity(): RepoActivity[] {
  const { data: reposData } = useQuery(reposQueryOptions);
  const repos = reposData?.repos ?? [];
  const { data: instData } = useQuery(instancesQueryOptions);
  const { data: costs } = useQuery(costsQueryOptions(1));
  const runsByRepo = useRunsByRepo(repos.map((r) => r.name));

  return repos.map((repo) => {
    const runs = runsByRepo.get(repo.name) ?? [];
    const byTicket = new Map<string, Run>(runs.map((r) => [r.ticket, r]));
    const loops = (instData?.instances ?? [])
      .filter((inst) => inst.repo === repo.name)
      .map((inst) =>
        makeLiveLoop(inst, inst.ticket ? byTicket.get(inst.ticket) : undefined),
      );
    const attention: AttentionRun[] = runs
      .filter((run) => run.failure_class)
      .map((run) => ({ ...run, repo: repo.name }));
    const cost = costs?.repos.find((c) => c.repo === repo.name);
    return {
      repo,
      loops,
      attention,
      spend: cost?.cost_usd ?? 0,
      metered: cost?.metered ?? true,
    };
  });
}

export function recentRuns(runs: Run[], limit = 6): Run[] {
  return [...runs]
    .sort((a, b) => (b.updated_at ?? "").localeCompare(a.updated_at ?? ""))
    .slice(0, limit);
}

const ACTIVE_STATES = new Set<SessionState>(["grazing", "working", "stopping"]);

export function isActiveState(state: SessionState): boolean {
  return ACTIVE_STATES.has(state);
}

export function activeLoopCount(loops: LiveLoop[]): number {
  return loops.reduce(
    (n, loop) => (isActiveState(loop.sessionState) ? n + 1 : n),
    0,
  );
}

export function attentionPill(cls: FailureClass): {
  state: RunState;
  label: string;
} {
  switch (cls) {
    case "paused":
      return { state: "warn", label: "paused" };
    case "stopped":
      return { state: "warn", label: "stopped" };
    case "faulted":
      return { state: "fail", label: "fault" };
    case "gave_up":
      return { state: "fail", label: "quarantined" };
  }
}

export function sessionStatePill(state: SessionState): {
  state: RunState;
  label: string;
} {
  switch (state) {
    case "working":
      return { state: "active", label: "working" };
    case "grazing":
      return { state: "active", label: "grazing" };
    case "parked":
      return { state: "warn", label: "parked" };
    case "stopping":
      return { state: "todo", label: "stopping…" };
    case "takeover":
      return { state: "info", label: "Taken over" };
    case "idle":
      return { state: "todo", label: "idle" };
    case "unknown":
      return { state: "todo", label: "unknown" };
  }
}

export type RepoBadgeState = "active" | "parked" | "idle" | "none";

export function repoBadgeState(states: SessionState[]): RepoBadgeState {
  if (states.length === 0) return "none";
  if (states.some(isActiveState)) return "active";
  if (states.some((s) => s === "parked" || s === "takeover")) return "parked";
  return "idle";
}

export interface LoopCardView {
  pill: { state: RunState; label: string };
  copy?: string;
  showStepper: boolean;
  showWatch: boolean;
  showStop: boolean;
  stopDisabled: boolean;
  dimmed: boolean;
}

export function loopCardView(
  state: SessionState,
  opts: {
    phase?: string;
    activity?: string;
    detail?: string;
    failureClass?: FailureClass;
  } = {},
): LoopCardView {
  switch (state) {
    case "working":
      return {
        pill: stepPill(opts.activity, opts.phase ?? ""),
        showStepper: true,
        showWatch: true,
        showStop: true,
        stopDisabled: false,
        dimmed: false,
      };
    case "grazing":
      return {
        pill: { state: "active", label: "grazing" },
        copy: "Grazing — picking the next ready ticket",
        showStepper: false,
        showWatch: false,
        showStop: true,
        stopDisabled: false,
        dimmed: false,
      };
    case "parked":
      return {
        pill: opts.failureClass
          ? attentionPill(opts.failureClass)
          : { state: "fail", label: "parked" },
        copy: "Parked on the recap — waiting for you",
        showStepper: false,
        showWatch: true,
        showStop: true,
        stopDisabled: false,
        dimmed: false,
      };
    case "idle":
      return {
        pill: { state: "todo", label: "idle" },
        copy: "TUI open at the menu — nothing live",
        showStepper: false,
        showWatch: false,
        showStop: true,
        stopDisabled: false,
        dimmed: true,
      };
    case "stopping":
      return {
        pill: { state: "todo", label: "stopping…" },
        showStepper: false,
        showWatch: false,
        showStop: true,
        stopDisabled: true,
        dimmed: false,
      };
    case "takeover":
      return {
        pill: { state: "info", label: "Taken over" },
        copy: "Taken over in a terminal — use Run next to hand it back",
        showStepper: false,
        showWatch: true,
        showStop: false,
        stopDisabled: false,
        dimmed: false,
      };
    case "unknown":
      return {
        pill: { state: "todo", label: "unknown" },
        copy: "This trau predates session reporting — upgrade + restart it",
        showStepper: false,
        showWatch: false,
        showStop: true,
        stopDisabled: false,
        dimmed: false,
      };
  }
}

export interface TodaySpend {
  cost: number;
  budget?: number;
  metered: boolean;
}

export function useTodaySpend(repo: string | null): TodaySpend {
  const { data } = useQuery(costsQueryOptions(1));
  const scoped = data?.repos.find((r) => r.repo === repo);
  return {
    cost: scoped?.cost_usd ?? 0,
    budget: scoped?.daily_budget_usd ?? data?.budget.daily_usd,
    metered: scoped?.metered ?? true,
  };
}
