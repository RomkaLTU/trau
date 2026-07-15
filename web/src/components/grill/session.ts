import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  activeSessionForIssue,
  grillSessionsQueryOptions,
  startGrillSession,
  type GrillListResponse,
  type GrillSession,
} from "@/lib/grill";

// GrillSessionState is a host's view of one issue's session: the resolved session to
// mount a conversation on, whether the list has settled enough to say there is none,
// and an explicit start the host runs. resolution never opens a session, so viewing
// or browsing an issue costs nothing; retry is present only when a start the host ran
// failed and can be offered again.
export interface GrillSessionState {
  session?: GrillSession;
  resolved: boolean;
  starting: boolean;
  error: Error | null;
  start: (seed?: string) => void;
  retry?: () => void;
}

// useGrillSession resolves an issue's grilling session and hands back an explicit
// start — it never opens one just by being mounted, so selecting or skimming an issue
// creates nothing. The whole conversation is server-side, so an issue reopened later
// rejoins its thread instead of starting a second, and start() no-ops once the issue
// has a live session. Hosts must key on issueId.
//
// The optimistic list write is deliberate. Invalidating instead would refetch the
// session as settled the moment an outcome applies, and a settled session reads as
// "no active session" — which would strand the just-finished issue back in a preview.
export function useGrillSession(repo: string, issueId: string): GrillSessionState {
  const queryClient = useQueryClient();
  const list = useQuery(grillSessionsQueryOptions(repo));
  const active = activeSessionForIssue(list.data?.sessions, issueId);

  const create = useMutation({
    mutationFn: (seed: string) => startGrillSession(repo, issueId, seed),
    onSuccess: (sess) => {
      queryClient.setQueryData<GrillListResponse>(["grill", repo], (prev) =>
        prev
          ? {
              ...prev,
              sessions: [sess, ...prev.sessions.filter((s) => s.id !== sess.id)],
            }
          : { repo, sessions: [sess] },
      );
    },
    onError: () =>
      void queryClient.invalidateQueries({ queryKey: ["grill", repo] }),
  });

  // start opens the session, seeding the opening user turn when given text. It stays a
  // no-op while a start is in flight or the issue already has a live session, so a
  // double click or a stale render can never spawn a second.
  const start = (seed = "") => {
    if (active || create.isPending) return;
    create.mutate(seed);
  };

  const listError = (list.error as Error) ?? null;
  const createError = active ? null : ((create.error as Error) ?? null);

  return {
    session: active ?? create.data,
    resolved: list.isSuccess,
    starting: create.isPending,
    error: listError ?? createError,
    start,
    retry: listError === null && createError !== null ? () => start() : undefined,
  };
}
