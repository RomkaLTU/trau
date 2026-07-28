import { useRef, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Flame, Loader2 } from "lucide-react";

import { useModeSuggestion } from "@/components/grill/mode-suggestion";
import { SessionTypeChips } from "@/components/grill/session-mode";
import { Button } from "@/components/ui/button";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import {
  isActiveSessionConflict,
  publishGrillSession,
  startGrillSession,
} from "@/lib/grill";

// StartInterviewDialog is how an interview begins on a ticket that has none: an
// optional note on what to clarify seeds the opening turn. The session exists
// before onStarted runs, so the caller lands on a live conversation — and so does a
// start the hub refused because a session raced it, since that one is the
// conversation to join.
export function StartInterviewDialog({
  repo,
  id,
  onStarted,
}: {
  repo: string;
  id: string;
  onStarted: () => void;
}) {
  const queryClient = useQueryClient();
  const fieldRef = useRef<HTMLTextAreaElement | null>(null);
  const [open, setOpen] = useState(false);
  const [focus, setFocus] = useState("");
  const sessionType = useModeSuggestion(repo);
  const research = sessionType.mode === "research";

  const start = useMutation({
    mutationFn: () =>
      startGrillSession(repo, id, {
        seed: focus.trim(),
        mode: sessionType.mode,
      }),
    onSuccess: (session) => {
      publishGrillSession(queryClient, repo, session);
      onStarted();
    },
    onError: (err) => {
      if (isActiveSessionConflict(err)) onStarted();
    },
  });

  const submit = () => {
    if (start.isPending) return;
    start.mutate();
  };

  const failure =
    start.error && !isActiveSessionConflict(start.error)
      ? start.error.message
      : "";

  return (
    <>
      <Button
        variant="outline"
        size="sm"
        onClick={() => {
          start.reset();
          setOpen(true);
        }}
      >
        <Flame />
        Interview
      </Button>
      <AlertDialog open={open} onOpenChange={setOpen}>
        <AlertDialogContent
          onOpenAutoFocus={(e) => {
            e.preventDefault();
            fieldRef.current?.focus();
          }}
          className="gap-0 overflow-hidden border-border bg-popover p-0 shadow-xl sm:max-w-md"
        >
          <div className="flex items-center gap-3 border-b border-border px-4 py-2.5">
            <div className="flex items-center gap-1.5" aria-hidden="true">
              <span className="size-2.5 rounded-full bg-fail" />
              <span className="size-2.5 rounded-full bg-warn" />
              <span className="size-2.5 rounded-full bg-done" />
            </div>
            <span className="font-mono text-xs text-muted-foreground">
              start-interview
            </span>
          </div>
          <div className="flex flex-col gap-2 p-4">
            <AlertDialogHeader className="gap-2 text-left">
              <AlertDialogTitle className="font-mono text-sm font-normal text-foreground">
                {research ? "Research" : "Interview"} {id}
              </AlertDialogTitle>
              <AlertDialogDescription className="font-sans text-sm leading-relaxed text-muted-foreground">
                {research
                  ? "The agent investigates against primary sources and delivers a findings report. A note says what to answer."
                  : "The interviewer reads the ticket and asks what it leaves open. A note steers where it starts."}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <SessionTypeChips sessionType={sessionType} className="mt-2" />
            <textarea
              ref={fieldRef}
              value={focus}
              rows={3}
              aria-label={
                research
                  ? `Research question for ${id}`
                  : `Interview focus for ${id}`
              }
              placeholder={
                research
                  ? "What do you want answered? (optional)"
                  : "What do you want clarified? (optional)"
              }
              onChange={(e) => {
                setFocus(e.target.value);
                sessionType.noteChanged(e.target.value);
              }}
              onBlur={(e) => sessionType.noteSettled(e.target.value)}
              onKeyDown={(e) => {
                if (e.key !== "Enter" || !(e.metaKey || e.ctrlKey)) return;
                e.preventDefault();
                submit();
              }}
              className="w-full resize-none rounded-md border border-border bg-input px-2.5 py-1.5 font-sans text-sm text-foreground outline-none placeholder:text-muted-foreground/60 focus-visible:border-ring"
            />
            {failure && (
              <p role="alert" className="font-mono text-xs text-fail">
                {failure}
              </p>
            )}
            <AlertDialogFooter className="mt-2">
              <AlertDialogCancel className="h-8 gap-1.5 px-3 font-mono text-sm">
                Cancel
              </AlertDialogCancel>
              <Button
                size="sm"
                className="h-8 gap-1.5 px-3 font-mono text-sm"
                disabled={start.isPending}
                onClick={submit}
              >
                {start.isPending && <Loader2 className="animate-spin" />}
                {research ? "Start research" : "Start interview"}
              </Button>
            </AlertDialogFooter>
          </div>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
