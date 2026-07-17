import { Archive, X } from "lucide-react";

// ArchiveToast is the reversible-action confirmation shown after an archive or
// unarchive — presentational only; the caller owns the dismiss timer, mirroring
// the backlog's created toast.
export function ArchiveToast({
  message,
  onDismiss,
}: {
  message: string;
  onDismiss: () => void;
}) {
  return (
    <div
      role="status"
      className="animate-in fade-in slide-in-from-bottom-2 fixed bottom-6 right-6 z-50 flex w-80 items-start gap-3 rounded-lg border bg-card p-3 shadow-lg"
    >
      <Archive
        className="mt-0.5 size-4 shrink-0 text-muted-foreground"
        aria-hidden
      />
      <p className="min-w-0 flex-1 text-sm text-foreground">{message}</p>
      <button
        type="button"
        onClick={onDismiss}
        aria-label="Dismiss"
        className="inline-flex size-6 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
      >
        <X className="size-4" aria-hidden />
      </button>
    </div>
  );
}
