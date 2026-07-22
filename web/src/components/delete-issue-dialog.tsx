import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Loader2, Trash2 } from "lucide-react";

import { ConfirmDialog } from "@/components/trau/confirm-dialog";
import { Button } from "@/components/ui/button";
import { deleteToastMessage, deleteWarning } from "@/lib/delete-issue";
import { deleteIssue, issueQueryOptions } from "@/lib/issues";
import { notificationsQueryKey } from "@/lib/notification-center";
import { cn } from "@/lib/utils";

// DeleteIssueDialog is the one destructive confirm behind every Delete. The purge
// takes the issue's board row, interviews, notifications and queue entries with it,
// so every list that could still be holding it is refreshed; onDeleted is left the
// surface's own follow-up, advancing a selection or closing a panel, and is handed
// every identifier that went so it can steer clear of an epic's children too.
// iconOnly drops the label for the dense inbox session bar.
export function DeleteIssueDialog({
  repo,
  id,
  iconOnly = false,
  onDeleted,
}: {
  repo: string;
  id: string;
  iconOnly?: boolean;
  onDeleted: (deleted: string[]) => void;
}) {
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);

  // The warning has to name the family the purge takes down, archived children
  // included, and only the issue read carries that count — the board's own
  // children_total leaves archived rows out. Read on open, so a rail of Delete
  // buttons costs nothing until one is pressed, and the confirm stays inert until
  // the answer is in.
  const issue = useQuery({ ...issueQueryOptions(repo, id), enabled: open });
  const target = issue.data
    ? { id, source: issue.data.source, children: issue.data.children }
    : null;

  const remove = useMutation({
    mutationFn: () => deleteIssue(repo, id),
    onSuccess: (result) => {
      toast.success(deleteToastMessage(id, result.deleted));
      onDeleted(result.deleted);
      void queryClient.invalidateQueries({ queryKey: ["backlog", repo] });
      void queryClient.invalidateQueries({ queryKey: ["grill", repo] });
      void queryClient.invalidateQueries({ queryKey: ["queue", repo] });
      void queryClient.invalidateQueries({ queryKey: ["issue-search", repo] });
      void queryClient.invalidateQueries({ queryKey: notificationsQueryKey });
    },
    onError: (err) => toast.error(err.message),
  });

  return (
    <>
      <Button
        variant="outline"
        size={iconOnly ? "icon" : "sm"}
        disabled={remove.isPending}
        onClick={() => setOpen(true)}
        aria-label={`Delete ${id}`}
        title={iconOnly ? `Delete ${id}` : undefined}
        className={cn(
          "border-destructive/40 text-destructive hover:border-destructive/60 hover:bg-destructive/10 hover:text-destructive",
          iconOnly && "size-8",
        )}
      >
        {remove.isPending ? <Loader2 className="animate-spin" /> : <Trash2 />}
        {!iconOnly && "Delete"}
      </Button>
      <ConfirmDialog
        open={open}
        onOpenChange={setOpen}
        windowTitle="delete issue"
        title={`Delete ${id}?`}
        description={
          target
            ? deleteWarning(target)
            : (issue.error?.message ?? "Checking what this deletes…")
        }
        confirmLabel="Delete forever"
        confirmDisabled={!target}
        destructive
        onConfirm={() => remove.mutate()}
      />
    </>
  );
}
