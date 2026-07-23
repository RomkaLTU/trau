import { useState, type ReactNode } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";

import { Button } from "@/components/ui/button";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import {
  handbackChoices,
  handbackStep,
  settleHandback,
  type Handback,
} from "@/lib/handback";

export function HandbackDialog({
  open,
  onOpenChange,
  ticket,
  handback,
  pending,
  error,
  onChoose,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  ticket: string;
  handback: Handback;
  pending: boolean;
  error: string;
  onChoose: (rerun: boolean) => void;
}) {
  const choices = handbackChoices(handback);
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent className="gap-0 overflow-hidden border-border bg-popover p-0 shadow-xl sm:max-w-md">
        <div className="flex items-center gap-3 border-b border-border px-4 py-2.5">
          <div className="flex items-center gap-1.5" aria-hidden="true">
            <span className="size-2.5 rounded-full bg-fail" />
            <span className="size-2.5 rounded-full bg-warn" />
            <span className="size-2.5 rounded-full bg-done" />
          </div>
          <span className="font-mono text-xs text-muted-foreground">
            hand-back
          </span>
        </div>
        <div className="flex flex-col gap-2 p-4">
          <AlertDialogHeader className="gap-2 text-left">
            <AlertDialogTitle className="font-mono text-sm font-normal text-foreground">
              Hand {ticket} back to the loop
            </AlertDialogTitle>
            <AlertDialogDescription className="font-sans text-sm leading-relaxed text-muted-foreground">
              You steered {ticket} in a terminal, so only you know how far{" "}
              {handbackStep(handback)} got. Pick where the loop picks up.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <div className="mt-2 flex flex-col gap-2">
            <Button
              size="sm"
              className="w-full justify-start font-mono"
              disabled={pending}
              onClick={() => onChoose(true)}
            >
              {choices.rerun}
            </Button>
            {choices.advance && (
              <Button
                variant="outline"
                size="sm"
                className="w-full justify-start whitespace-normal text-left font-mono"
                disabled={pending}
                onClick={() => onChoose(false)}
              >
                {choices.advance}
              </Button>
            )}
          </div>
          {error && (
            <p className="mt-1 font-mono text-xs text-fail">{error}</p>
          )}
          <div className="mt-3 flex justify-end">
            <AlertDialogCancel className="h-8 gap-1.5 px-3 font-mono text-sm">
              Cancel
            </AlertDialogCancel>
          </div>
        </div>
      </AlertDialogContent>
    </AlertDialog>
  );
}

// useHandback wraps a hand-back gesture in the one-time choice a takeover leaves
// behind: a ticket no terminal steered goes straight through to onProceed, one
// that was steered first asks whether the interrupted phase is done and settles
// the stamp before handing the ticket back.
export function useHandback(
  repo: string,
  onProceed: (ticket: string) => void,
): { request: (ticket: string, handback: Handback | null) => void; dialog: ReactNode } {
  const queryClient = useQueryClient();
  const [choice, setChoice] = useState<{
    ticket: string;
    handback: Handback;
  } | null>(null);

  const settle = useMutation({
    mutationFn: ({ ticket, rerun }: { ticket: string; rerun: boolean }) =>
      settleHandback(repo, ticket, rerun),
    onSuccess: (_res, { ticket }) => {
      void queryClient.invalidateQueries({ queryKey: ["runs", repo] });
      void queryClient.invalidateQueries({ queryKey: ["run", repo, ticket] });
      setChoice(null);
      onProceed(ticket);
    },
  });

  const request = (ticket: string, handback: Handback | null) => {
    if (!handback) {
      onProceed(ticket);
      return;
    }
    settle.reset();
    setChoice({ ticket, handback });
  };

  const dialog = choice ? (
    <HandbackDialog
      open
      onOpenChange={(next) => {
        if (!next) setChoice(null);
      }}
      ticket={choice.ticket}
      handback={choice.handback}
      pending={settle.isPending}
      error={
        settle.error
          ? settle.error instanceof Error
            ? settle.error.message
            : String(settle.error)
          : ""
      }
      onChoose={(rerun) => settle.mutate({ ticket: choice.ticket, rerun })}
    />
  ) : null;

  return { request, dialog };
}
