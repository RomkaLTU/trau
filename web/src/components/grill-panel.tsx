import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Loader2, Sparkles } from "lucide-react";

import { ErrorNote, statePill } from "@/components/grill/banners";
import {
  GrillConversation,
  type GrillStatus,
} from "@/components/grill/conversation";
import { useGrillSession } from "@/components/grill/session";
import { Button } from "@/components/ui/button";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { StatusPill, type RunState } from "@/components/trau";
import {
  startGrillSession,
  type GrillListResponse,
  type GrillSession,
} from "@/lib/grill";

// GrillPanel is the chat surface for one issue's grilling session, mounted in the
// backlog drawer. It reopens the issue's live session if one exists, otherwise
// starts one — the whole conversation is server-side, so closing and reopening the
// drawer restores the thread and any pending question.
export function GrillPanel({
  repo,
  issueId,
  onClose,
  onApplied,
}: {
  repo: string;
  issueId: string;
  onClose: () => void;
  // onApplied fires once an outcome fully lands on the tracker, so the triage
  // inbox can auto-advance to the next unclear issue.
  onApplied?: () => void;
}) {
  const { session, starting, error, retry } = useGrillSession(repo, issueId);

  if (session) {
    return (
      <FramedConversation
        key={session.id}
        repo={repo}
        session={session}
        onClose={onClose}
        onApplied={onApplied}
      />
    );
  }

  return (
    <PanelFrame onClose={onClose}>
      <div className="flex flex-1 items-center justify-center px-4">
        {error ? (
          <div className="flex flex-col items-center gap-3">
            <ErrorNote message={error.message} />
            {retry && (
              <Button size="sm" variant="outline" onClick={retry}>
                Try again
              </Button>
            )}
          </div>
        ) : (
          <p className="inline-flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="size-4 animate-spin" />
            {starting ? "Starting grilling session…" : "Loading…"}
          </p>
        )}
      </div>
    </PanelFrame>
  );
}

// AuthoringPanel is the from-scratch entry: the user types a one-line idea, a
// repo-anchored authoring session starts, and the same conversation surface takes
// over — ending in a create proposal reviewed before anything is filed.
export function AuthoringPanel({
  repo,
  onClose,
  onCreated,
}: {
  repo: string;
  onClose: () => void;
  // onCreated fires once a create outcome lands on the tracker, so the caller can
  // refresh the board.
  onCreated?: () => void;
}) {
  const queryClient = useQueryClient();
  const [idea, setIdea] = useState("");

  const create = useMutation({
    mutationFn: (seed: string) => startGrillSession(repo, "", seed),
    onSuccess: (sess) => {
      queryClient.setQueryData<GrillListResponse>(["grill", repo], (prev) =>
        prev
          ? {
              ...prev,
              sessions: [
                sess,
                ...prev.sessions.filter((s) => s.id !== sess.id),
              ],
            }
          : { repo, sessions: [sess] },
      );
    },
  });

  if (create.data) {
    return (
      <FramedConversation
        key={create.data.id}
        repo={repo}
        session={create.data}
        onClose={onClose}
        onApplied={onCreated}
      />
    );
  }

  const start = () => {
    const seed = idea.trim();
    if (seed === "" || create.isPending) return;
    create.mutate(seed);
  };

  return (
    <PanelFrame onClose={onClose}>
      <div className="flex flex-1 flex-col gap-3 px-4 py-4">
        <p className="text-sm text-muted-foreground">
          Describe the idea in a line or two. A repo-aware agent will interview
          you toward a fully-specified issue, then propose it for review before
          anything is filed.
        </p>
        <textarea
          value={idea}
          onChange={(e) => setIdea(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
              e.preventDefault();
              start();
            }
          }}
          placeholder="e.g. Add a dark-mode toggle to the settings page"
          rows={3}
          className="w-full resize-y rounded-md border bg-card px-3 py-2 text-sm outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
        />
        <div>
          <Button
            size="sm"
            onClick={start}
            disabled={idea.trim() === "" || create.isPending}
          >
            {create.isPending ? (
              <Loader2 className="animate-spin" />
            ) : (
              <Sparkles />
            )}
            Start grilling
          </Button>
        </div>
        {create.error && (
          <ErrorNote message={(create.error as Error).message} />
        )}
      </div>
    </PanelFrame>
  );
}

// AuthoringDrawer hosts a from-scratch grilling session in the same right-side
// offcanvas the issue drawer uses — no anchor issue, just the repo and an idea.
// Shared by the backlog board and the triage inbox.
export function AuthoringDrawer({
  repo,
  open,
  onOpenChange,
  onCreated,
}: {
  repo: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated?: () => void;
}) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="w-full gap-0 p-0 sm:max-w-xl">
        <SheetHeader className="sr-only">
          <SheetTitle>New grilled issue</SheetTitle>
        </SheetHeader>
        {open && (
          <AuthoringPanel
            repo={repo}
            onClose={() => onOpenChange(false)}
            onCreated={onCreated}
          />
        )}
      </SheetContent>
    </Sheet>
  );
}

// FramedConversation hangs the frame-agnostic conversation in the drawer chrome,
// driving the header's pill and reconnecting note from the status it reports.
function FramedConversation({
  repo,
  session,
  onClose,
  onApplied,
}: {
  repo: string;
  session: GrillSession;
  onClose: () => void;
  onApplied?: () => void;
}) {
  const [status, setStatus] = useState<GrillStatus>({
    stream: "connecting",
    session,
  });
  return (
    <PanelFrame
      onClose={onClose}
      pill={statePill(status.session.state)}
      reconnecting={status.stream === "error"}
    >
      <GrillConversation
        repo={repo}
        initial={session}
        onStatus={setStatus}
        onApplied={onApplied}
      />
    </PanelFrame>
  );
}

function PanelFrame({
  onClose,
  pill,
  reconnecting,
  children,
}: {
  onClose: () => void;
  pill?: { state: RunState; label: string };
  reconnecting?: boolean;
  children: React.ReactNode;
}) {
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex items-center gap-2 border-b px-4 py-2.5">
        <Button variant="ghost" size="sm" onClick={onClose} className="-ml-2">
          <ArrowLeft />
          Back
        </Button>
        <div className="flex-1" />
        {reconnecting && (
          <span className="inline-flex items-center gap-1 text-xs text-warn">
            <span aria-hidden="true">⚠</span>
            reconnecting…
          </span>
        )}
        {pill && <StatusPill state={pill.state} label={pill.label} />}
      </div>
      {children}
    </div>
  );
}
