import { queryOptions } from "@tanstack/react-query";

import { apiFetch } from "./api";

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
