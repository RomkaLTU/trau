import { queryOptions, useQuery } from "@tanstack/react-query";

import type { RunState } from "@/components/trau/status-pill";
import { apiFetch } from "./api";

export interface Instance {
  pid: number;
  repo: string;
  repo_root: string;
  runs_dir: string;
  started_at: string;
  session_state: string;
  ticket?: string;
  phase?: string;
  activity?: string;
  detail?: string;
  state_since?: string;
}

// RepoHealthState mirrors the hub's derived health. A recorded error is
// sync-failed even over a good synced stamp, so a repo whose seed sync failed
// never reads as ready.
export type RepoHealthState =
  "ready" | "unconfigured" | "sync-failed" | "never-synced" | "syncing";

// RepoFreshness is a repo's issue-store sync state: when it last synced from the
// tracker, whether a background sync is running right now, the last sync error,
// and the counts the last good sync wrote. Absent for a repo that has never
// synced and is not syncing. The repos API always carries a state and issue_count;
// the backlog attaches only the sync fields.
export interface RepoFreshness {
  state?: RepoHealthState;
  last_synced_at?: string;
  syncing: boolean;
  last_error?: string;
  last_issues?: number;
  last_comments?: number;
  issue_count?: number;
}

export interface RepoView {
  name: string;
  root: string;
  runs_dir: string;
  live: boolean;
  allowed: boolean;
  registered: boolean;
  seeded: boolean;
  freshness?: RepoFreshness;
}

// RepoHealth is one repo's /health resource: the derived state plus the sync
// facts behind it, so a gate can poll a single repo instead of the whole list.
// provider is the tracker the state came from — empty or "internal" means there
// is nothing to pull.
export interface RepoHealth {
  repo: string;
  provider: string;
  state: RepoHealthState;
  last_synced_at: string;
  last_error: string;
  issue_count: number;
}

export interface InstancesResponse {
  instances: Instance[];
  repos: RepoView[];
  takeover_supported?: boolean;
}

// Only a store read error drops the freshness the repos API otherwise always
// sends; that repo reads as unconfigured rather than claiming health.
export function repoHealth(repo: RepoView): RepoHealthState {
  return repo.freshness?.state ?? "unconfigured";
}

export function anySyncing(repos: readonly RepoView[]): boolean {
  return repos.some((repo) => repoHealth(repo) === "syncing");
}

export function healthPill(state: RepoHealthState): {
  state: RunState;
  label: string;
} {
  switch (state) {
    case "ready":
      return { state: "success", label: "ready" };
    case "syncing":
      return { state: "active", label: "syncing" };
    case "sync-failed":
      return { state: "fail", label: "sync failing" };
    case "never-synced":
      return { state: "warn", label: "never synced" };
    case "unconfigured":
      return { state: "warn", label: "not configured" };
  }
}

// healthBlocks reports whether a state stops a repo-scoped page from being
// trusted: nothing is configured to fetch, or the last sync recorded an error.
// A syncing or never-synced repo is mid-setup and left alone.
export function healthBlocks(state: RepoHealthState): boolean {
  return state === "unconfigured" || state === "sync-failed";
}

async function fetchInstances(): Promise<InstancesResponse> {
  const res = await apiFetch("/api/v1/instances");
  if (!res.ok) {
    throw new Error(`instances request failed: ${res.status}`);
  }
  return res.json();
}

export const instancesQueryOptions = queryOptions({
  queryKey: ["instances"],
  queryFn: fetchInstances,
  refetchInterval: 3000,
  // Keep polling in a backgrounded tab so the live tab title stays current while
  // a user watches a run from another tab.
  refetchIntervalInBackground: true,
});

// TAKEOVER_BLOCKED is what every control a live takeover gates says, both as a
// tooltip and as the rendering of the hub's 409 (ADR 0018).
export const TAKEOVER_BLOCKED =
  "Taken over in a terminal — close it to hand the repo back";

export function repoTakenOver(
  instances: readonly Instance[],
  repo: string,
): boolean {
  return instances.some(
    (i) => i.repo === repo && i.session_state === "takeover",
  );
}

// useRepoTakenOver rides the shared instances poll, so a gated control re-enables
// on its own once the takeover deregisters.
export function useRepoTakenOver(repo: string): boolean {
  const { data } = useQuery(instancesQueryOptions);
  return data ? repoTakenOver(data.instances, repo) : false;
}

export function repoRoot(repos: readonly RepoView[], repo: string): string {
  return repos.find((r) => r.name === repo)?.root ?? "";
}

// useRepoRoot rides the shared instances poll, so a surface that needs the repo's
// absolute path gets it without a fetch of its own.
export function useRepoRoot(repo: string): string {
  const { data } = useQuery(instancesQueryOptions);
  return data ? repoRoot(data.repos, repo) : "";
}

async function fetchRepoHealth(repo: string): Promise<RepoHealth> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/health`,
  );
  if (!res.ok) {
    throw new Error(`repo health request failed: ${res.status}`);
  }
  return res.json();
}

// Keyed by repo so every gate on a page shares one fetch rather than one per
// section. It polls because the pages behind it stay open for hours: without an
// interval a background sync that started failing would go unnoticed until the
// next focus or remount.
export const repoHealthQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ["repo-health", repo],
    queryFn: () => fetchRepoHealth(repo),
    enabled: repo !== "",
    staleTime: 15_000,
    refetchInterval: 30_000,
  });

async function errorMessage(res: Response, fallback: string): Promise<string> {
  const detail = (await res.json().catch(() => null)) as {
    error?: string;
  } | null;
  return detail?.error ?? `${fallback}: ${res.status}`;
}

export interface StopResult {
  status: string;
  signal: string;
}

export async function stopInstance(pid: number): Promise<StopResult> {
  const res = await apiFetch(`/api/v1/instances/${pid}/stop`, {
    method: "POST",
  });
  if (!res.ok) {
    throw new Error(await errorMessage(res, "stop failed"));
  }
  return res.json();
}

export class TakeoverError extends Error {
  status: number;

  constructor(message: string, status: number) {
    super(message);
    this.name = "TakeoverError";
    this.status = status;
  }
}

export interface TakeoverResult {
  stopped: boolean;
  opened: boolean;
}

// takeoverRun hands a run to a terminal (ADR 0018). On a live run the endpoint
// blocks through the graceful stop — up to 60s — so the fetch timeout sits past
// that bound instead of aborting mid-stop.
export async function takeoverRun(
  repo: string,
  ticket: string,
): Promise<TakeoverResult> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/runs/${encodeURIComponent(
      ticket,
    )}/takeover`,
    { method: "POST", signal: AbortSignal.timeout(75_000) },
  );
  if (!res.ok) {
    throw new TakeoverError(
      await errorMessage(res, "takeover failed"),
      res.status,
    );
  }
  return res.json();
}

export async function registerRepo(path: string): Promise<RepoView> {
  const res = await apiFetch("/api/v1/repos", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ path }),
  });
  if (!res.ok) {
    throw new Error(await errorMessage(res, "register failed"));
  }
  return res.json();
}

// unregisterRepo and forgetRepo both address the repo by ident — its name, or its
// root path, which is the only way to name one of two repos sharing a basename.
export async function unregisterRepo(ident: string): Promise<RepoView> {
  const res = await apiFetch(`/api/v1/repos/${encodeURIComponent(ident)}`, {
    method: "DELETE",
  });
  if (!res.ok) {
    throw new Error(await errorMessage(res, "unregister failed"));
  }
  return res.json();
}

// forgetRepo drops the repo off the hub's list entirely, the one removal that
// also reaches a repo the hub merely observed a loop run in.
export async function forgetRepo(ident: string): Promise<RepoView> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(ident)}?forget=1`,
    { method: "DELETE" },
  );
  if (!res.ok) {
    throw new Error(await errorMessage(res, "remove failed"));
  }
  return res.json();
}

// removeBlocked reports why the hub would refuse to remove this repo, so the row
// says so up front instead of handing back a toast. It reads the seeded flag the
// hub sends rather than inferring the grant from a missing registration, which a
// repo that is both seeded and registered would read the wrong way round. null
// means the removal goes through.
export function removeBlocked(repo: RepoView): string | null {
  if (repo.live) {
    return "A loop is live here — stop it before removing the repo";
  }
  if (repo.seeded) {
    return "Granted by SERVE_WORKSPACE — drop its root from the config instead";
  }
  return null;
}

export interface SyncResponse {
  repo: string;
  provider: string;
  issues: number;
  comments: number;
  removed: number;
  syncedAt: string;
}

// syncRepo pulls the repo's tracker project into the hub issue store and sweeps
// the issues the tracker no longer returns, blocking for the length of both.
export async function syncRepo(repo: string): Promise<SyncResponse> {
  const res = await apiFetch(`/api/v1/repos/${encodeURIComponent(repo)}/sync`, {
    method: "POST",
  });
  if (!res.ok) {
    throw new Error(await errorMessage(res, "sync failed"));
  }
  return res.json();
}

export interface DryRunResult {
  repo: string;
  repo_root: string;
  ticket: string;
}

export async function dryRun(repo: string): Promise<DryRunResult> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/dry-run`,
    { method: "POST" },
  );
  if (!res.ok) {
    throw new Error(await errorMessage(res, "dry-run failed"));
  }
  return res.json();
}
