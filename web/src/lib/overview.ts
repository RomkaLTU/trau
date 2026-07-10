import { useQuery } from "@tanstack/react-query";

import type { RunState } from "@/components/trau/status-pill";
import { costsQueryOptions } from "./costs";
import { instancesQueryOptions } from "./instances";
import { runsQueryOptions, type FailureClass, type Run } from "./runs";

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

const PHASE_SEQUENCE: { label: string; min: number; max: number }[] = [
  { label: "build", min: 1, max: 2 },
  { label: "handoff", min: 3, max: 3 },
  { label: "verify", min: 4, max: 4 },
  { label: "pr", min: 5, max: 5 },
  { label: "merge", min: 6, max: 6 },
];

export type PhaseState = "done" | "active" | "todo";

export interface PhaseStep {
  label: string;
  state: PhaseState;
}

export function phaseSteps(phase: string): PhaseStep[] {
  const rank = phaseRank(phase);
  return PHASE_SEQUENCE.map(({ label, min, max }) => ({
    label,
    state: rank > max ? "done" : rank >= min ? "active" : "todo",
  }));
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

export type SessionState =
  "working" | "grazing" | "parked" | "idle" | "stopping" | "unknown";

export interface LiveLoop {
  repo: string;
  pid: number;
  ticket?: string;
  title?: string;
  sessionState: SessionState;
  phase: string;
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
      return raw;
    default:
      return "unknown";
  }
}

export function useLiveLoops(repo: string | null): LiveLoop[] {
  const { data } = useQuery(instancesQueryOptions);
  const instances = (data?.instances ?? []).filter((i) => i.repo === repo);

  const { data: runs } = useQuery(runsQueryOptions(repo ?? ""));
  const byTicket = new Map<string, Run>();
  for (const run of runs?.runs ?? []) {
    byTicket.set(run.ticket, run);
  }

  return instances.map((inst) => {
    const state = toSessionState(inst.session_state);
    const run = inst.ticket ? byTicket.get(inst.ticket) : undefined;
    return {
      repo: inst.repo,
      pid: inst.pid,
      ticket: inst.ticket,
      title: run?.title,
      sessionState: state,
      phase: state === "working" ? (inst.phase ?? "") : "",
      startedAt: inst.started_at,
      stateSince: inst.state_since,
      failureClass: run?.failure_class,
      failureReason: run?.failure_reason,
    };
  });
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
  if (states.some((s) => s === "parked")) return "parked";
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
  opts: { phase?: string; failureClass?: FailureClass } = {},
): LoopCardView {
  switch (state) {
    case "working":
      return {
        pill: phasePill(opts.phase ?? ""),
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
