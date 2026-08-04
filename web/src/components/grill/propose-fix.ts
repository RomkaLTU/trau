import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  activeFixSessionForIssue,
  grillSessionsQueryOptions,
  isActiveSessionConflict,
  publishGrillSession,
  startGrillSession,
  type GrillSession,
} from "@/lib/grill";

// ProposeFix is a surface's view of one failed run's fix session: the live one when
// the ticket already has it — which the surface links to rather than starting a
// second — and the act that opens one when it does not.
export interface ProposeFix {
  session?: GrillSession;
  starting: boolean;
  error: string;
  start: () => void;
}

// useProposeFix drives the Propose fix action on a quarantined or faulted run. The
// session opens auto-accepting and with no seed of its own: the hub compiles the run's
// failure dossier at create and the first turn is grounded in that. A start the hub
// refused because a session raced it still lands the caller on the Inbox, since that
// session is the one to join.
export function useProposeFix({
  repo,
  ticket,
  enabled,
  onStarted,
}: {
  repo: string;
  ticket: string;
  enabled: boolean;
  onStarted: () => void;
}): ProposeFix {
  const queryClient = useQueryClient();
  const list = useQuery({ ...grillSessionsQueryOptions(repo), enabled });
  const start = useMutation({
    mutationFn: () =>
      startGrillSession(repo, ticket, { mode: "fix", autoAccept: true }),
    onSuccess: (session) => {
      publishGrillSession(queryClient, repo, session);
      onStarted();
    },
    onError: (err) => {
      if (isActiveSessionConflict(err)) onStarted();
    },
  });

  return {
    session: activeFixSessionForIssue(list.data?.sessions, ticket),
    starting: start.isPending,
    error:
      start.error && !isActiveSessionConflict(start.error)
        ? start.error.message
        : "",
    start: () => start.mutate(),
  };
}

// proposeFixable reports whether a run is one a fix session has anything to diagnose:
// a verified dead end or an unexpected fault, and nothing still working on it.
export function proposeFixable(failureClass?: string, live?: boolean): boolean {
  return !live && (failureClass === "gave_up" || failureClass === "faulted");
}
