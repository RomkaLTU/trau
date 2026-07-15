import { useEffect, useRef } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  activeSessionForIssue,
  grillSessionsQueryOptions,
  startGrillSession,
  type GrillListResponse,
  type GrillSession,
} from "@/lib/grill";

// GrillSessionState is a host's view of one issue's session: the session to mount a
// conversation on once there is one, and why there isn't yet. retry is present only
// when the failure is a start the host can offer to run again.
export interface GrillSessionState {
  session?: GrillSession;
  starting: boolean;
  error: Error | null;
  retry?: () => void;
}

// useGrillSession resolves an issue's grilling session, opening one when the issue
// has none — the whole conversation is server-side, so an issue reopened later
// rejoins its thread instead of starting a second. Hosts must key on issueId: the
// started guard is per-mount.
//
// The optimistic list write is deliberate. Invalidating instead would refetch the
// session as settled the moment an outcome applies, and a settled session reads as
// "no active session" — which would trip the auto-start into grilling the issue the
// user just finished.
export function useGrillSession(repo: string, issueId: string): GrillSessionState {
  const queryClient = useQueryClient();
  const list = useQuery(grillSessionsQueryOptions(repo));
  const active = activeSessionForIssue(list.data?.sessions, issueId);
  const started = useRef(false);

  const create = useMutation({
    mutationFn: () => startGrillSession(repo, issueId),
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

  useEffect(() => {
    if (!list.isSuccess || active || started.current) return;
    started.current = true;
    create.mutate();
  }, [list.isSuccess, active]);

  const start = () => {
    started.current = true;
    create.mutate();
  };

  const listError = (list.error as Error) ?? null;
  const createError = active ? null : ((create.error as Error) ?? null);

  return {
    session: active ?? create.data,
    starting: create.isPending,
    error: listError ?? createError,
    retry: listError === null && createError !== null ? start : undefined,
  };
}
