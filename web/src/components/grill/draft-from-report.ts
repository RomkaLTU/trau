import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";

import { draftItemId } from "@/lib/inbox";
import { publishGrillSession, startGrillSession } from "@/lib/grill";

// DraftFromReport is the Draft an issue act on an open research report: whether a
// draft is on its way, and the click that starts one.
export interface DraftFromReport {
  drafting: boolean;
  draft: () => void;
}

// useDraftFromReport carries a finished report onward into work. It opens an
// unanchored interview seeded with the report — the hub writes the opening note and
// grounds the first turn in the findings — and lands the user on the Inbox with that
// draft selected, the way Propose fix lands them on the session it started. The
// report itself is untouched, so clicking again simply drafts a second one.
export function useDraftFromReport({
  repo,
  sessionId,
}: {
  repo: string;
  sessionId: string;
}): DraftFromReport {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const start = useMutation({
    mutationFn: () =>
      startGrillSession(repo, "", {
        mode: "interview",
        fromSession: sessionId,
      }),
    onSuccess: (session) => {
      publishGrillSession(queryClient, repo, session);
      void navigate({
        to: "/inbox",
        search: { issue: draftItemId(session.id), repo },
      });
    },
    onError: (err: Error) => toast.error(err.message),
  });

  return { drafting: start.isPending, draft: () => start.mutate() };
}
