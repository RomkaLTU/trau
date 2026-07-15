import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  abandonGrill,
  activeSessionForIssue,
  grillSessionsQueryOptions,
  isSettled,
  startGrillSession,
  type GrillListResponse,
  type GrillSession,
} from "@/lib/grill";

// GrillSessionState is a host's view of one issue's session: the resolved session to
// mount a conversation on, whether the list has settled enough to say there is none,
// and the explicit acts the host runs — start opens one, startOver discards the live
// session and opens a fresh one. resolution never opens a session, so viewing or
// browsing an issue costs nothing; retry is present only when a start the host ran
// failed and can be offered again.
export interface GrillSessionState {
  session?: GrillSession;
  resolved: boolean;
  starting: boolean;
  restarting: boolean;
  error: Error | null;
  start: (seed?: string) => void;
  startOver: () => void;
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

  // restart abandons the issue's live session and opens a fresh one in a single act, so
  // one deliberate Start over discards a derailed Interview and begins again. The old
  // session settles as abandoned server-side; the optimistic write settles it in the
  // list too, so resolution never reads the discard as "no session" and strands the
  // item back in a preview.
  const restart = useMutation({
    mutationFn: async (sid: string) => {
      await abandonGrill(sid);
      return startGrillSession(repo, issueId, "");
    },
    onSuccess: (sess) => {
      queryClient.setQueryData<GrillListResponse>(["grill", repo], (prev) => {
        const settled = (prev?.sessions ?? []).map((s) =>
          s.issue_id === issueId && !isSettled(s.state)
            ? { ...s, state: "abandoned" as const }
            : s,
        );
        return { repo, sessions: [sess, ...settled.filter((s) => s.id !== sess.id)] };
      });
    },
    onError: () =>
      void queryClient.invalidateQueries({ queryKey: ["grill", repo] }),
  });

  // startOver only fires when a live session exists to discard, and stays a no-op while
  // one restart is already in flight.
  const startOver = () => {
    if (!active || restart.isPending) return;
    restart.mutate(active.id);
  };

  const listError = (list.error as Error) ?? null;
  const createError = active ? null : ((create.error as Error) ?? null);

  return {
    session: active ?? create.data,
    resolved: list.isSuccess,
    starting: create.isPending,
    restarting: restart.isPending,
    error: listError ?? createError,
    start,
    startOver,
    retry: listError === null && createError !== null ? () => start() : undefined,
  };
}
