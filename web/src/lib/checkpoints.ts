import { queryOptions } from "@tanstack/react-query";

import { apiFetch } from "./api";
import { TAKEOVER_BLOCKED } from "./instances";

// RunCheckpoint is the raw checkpoint resource; data carries the loop's state
// keys verbatim (PHASE, BRANCH, SESSION, …), so SESSION here is the resumable
// claude session handle terminal takeover keys off (ADR 0018).
export interface RunCheckpoint {
  ticket: string;
  phase: string;
  data: Record<string, string>;
}

async function fetchRunCheckpoint(
  repo: string,
  ticket: string,
): Promise<RunCheckpoint | null> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/runs/${encodeURIComponent(
      ticket,
    )}/checkpoint`,
  );
  if (res.status === 404) return null;
  if (!res.ok) {
    throw new Error(`checkpoint request failed: ${res.status}`);
  }
  return res.json();
}

export const runCheckpointQueryOptions = (repo: string, ticket: string) =>
  queryOptions({
    queryKey: ["run-checkpoint", repo, ticket],
    queryFn: () => fetchRunCheckpoint(repo, ticket),
    refetchInterval: 5000,
    enabled: repo !== "" && ticket !== "",
  });

export interface ResetResult {
  status: string;
  ticket: string;
}

export interface ClearResult {
  status: string;
  ticket: string;
  was: string;
}

export interface ReconcileResult {
  repo: string;
  reconciled: string[];
}

// CheckpointError carries the machine-readable flags the run views branch on: a
// live-instance refusal, the takeover terminal no web button can stop, and the
// merged-ticket case the UI escalates to a forced confirmation.
export class CheckpointError extends Error {
  live: boolean;
  takenOver: boolean;
  requiresForce: boolean;

  constructor(
    message: string,
    opts: { live?: boolean; takenOver?: boolean; requiresForce?: boolean },
  ) {
    super(message);
    this.name = "CheckpointError";
    this.live = opts.live ?? false;
    this.takenOver = opts.takenOver ?? false;
    this.requiresForce = opts.requiresForce ?? false;
  }
}

// checkpointErrorText renders a refusal for a human. A takeover conflict — what
// a stale tab races into after the terminal opened — reads as the same line the
// gated controls carry, not the hub's raw text.
export function checkpointErrorText(error: unknown): string {
  if (error instanceof CheckpointError && error.takenOver)
    return TAKEOVER_BLOCKED;
  return error instanceof Error ? error.message : String(error);
}

async function post<T>(url: string, body?: unknown): Promise<T> {
  const res = await apiFetch(url, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    const detail = (await res.json().catch(() => null)) as {
      error?: string;
      live?: boolean;
      reason?: string;
      requires_force?: boolean;
    } | null;
    throw new CheckpointError(
      detail?.error ?? `request failed: ${res.status}`,
      {
        live: detail?.live,
        takenOver: detail?.reason === "taken_over",
        requiresForce: detail?.requires_force,
      },
    );
  }
  return res.json();
}

export function resetRun(
  repo: string,
  ticket: string,
  force: boolean,
): Promise<ResetResult> {
  return post(
    `/api/v1/repos/${encodeURIComponent(repo)}/runs/${encodeURIComponent(
      ticket,
    )}/reset`,
    { force },
  );
}

export function clearRun(repo: string, ticket: string): Promise<ClearResult> {
  return post(
    `/api/v1/repos/${encodeURIComponent(repo)}/runs/${encodeURIComponent(
      ticket,
    )}/clear`,
  );
}

export function reconcileRepo(repo: string): Promise<ReconcileResult> {
  return post(`/api/v1/repos/${encodeURIComponent(repo)}/reconcile`);
}
