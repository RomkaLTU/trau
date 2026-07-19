import { CheckCircle2, X } from "lucide-react";

// CreatedToast confirms a freshly filed issue — presentational only; the caller
// owns the dismiss timer, mirroring ArchiveToast. Manual backlog creation and an
// inbox create apply share it so the two creation flows look identical.
export function CreatedToast({
  id,
  title,
  actionLabel = "View issue",
  onView,
  onDismiss,
}: {
  id: string;
  title: string;
  actionLabel?: string;
  onView: () => void;
  onDismiss: () => void;
}) {
  return (
    <div
      role="status"
      className="animate-in fade-in slide-in-from-bottom-2 fixed bottom-6 right-6 z-50 flex w-80 items-start gap-3 rounded-lg border bg-card p-3 shadow-lg"
    >
      <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-done" aria-hidden />
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <p className="text-sm text-foreground">
          <span className="font-mono font-medium">{id}</span> created
        </p>
        <p className="truncate text-sm text-muted-foreground">{title}</p>
        <button
          type="button"
          onClick={onView}
          className="self-start pt-1 text-xs font-medium text-primary underline-offset-2 hover:underline"
        >
          {actionLabel}
        </button>
      </div>
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
