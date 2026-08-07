import { useState, type ReactNode } from "react";
import { Check, ChevronRight, Lock } from "lucide-react";

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
  PIPELINE_STEPS,
  SKIP_VERIFY,
  canonicalSkips,
  skipNames,
  toggleSkip,
  type PipelineActivity,
} from "@/lib/skips";
import { cn } from "@/lib/utils";

// RunStepsTarget is one pending run gesture the picker is standing in front of:
// the repo it lands in, the id it runs, the skip set the item already carries,
// and the label the confirm button wears when nothing is unticked. A launch that
// covers more than one item — a batch, the whole queue — names itself with title
// instead, since there is no single id to head the dialog with.
export interface RunStepsTarget {
  repo: string;
  id: string;
  title?: string;
  skips?: string[];
  confirmLabel: string;
  note?: ReactNode;
}

// UNION_NOTE says what a multi-item launch does with the choices already stored
// on its items: this dialog adds to them, it never clears one.
export const UNION_NOTE =
  "Disabled phases apply in addition to any choices made when items were queued.";

function ActivityRow({
  activity,
  enabled,
  onChange,
}: {
  activity: PipelineActivity;
  enabled: boolean;
  onChange: (enabled: boolean) => void;
}) {
  const label = (
    <span className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
      <span
        className={cn(
          "font-mono text-xs",
          enabled ? "text-foreground" : "text-muted-foreground line-through",
        )}
      >
        {activity.label}
      </span>
      {activity.caption && (
        <span className="text-xs text-muted-foreground">
          {activity.caption}
        </span>
      )}
    </span>
  );

  if (!activity.skip) {
    return (
      <div className="flex items-start gap-2 text-left">
        <span
          className="mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-sm border border-border bg-muted text-muted-foreground"
          aria-hidden="true"
        >
          <Lock className="size-2.5" />
        </span>
        {label}
        <span className="sr-only">always runs</span>
      </div>
    );
  }

  return (
    <button
      type="button"
      role="checkbox"
      aria-checked={enabled}
      onClick={() => onChange(!enabled)}
      className="flex items-start gap-2 text-left"
    >
      <span
        className={cn(
          "mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-sm border transition-colors",
          enabled
            ? "border-primary bg-primary text-primary-foreground"
            : "border-border bg-input",
        )}
        aria-hidden="true"
      >
        {enabled && <Check className="size-3" />}
      </span>
      {label}
    </button>
  );
}

export function PipelineStepList({
  skips,
  onChange,
}: {
  skips: string[];
  onChange: (skips: string[]) => void;
}) {
  return (
    <div className="flex flex-col gap-3">
      {PIPELINE_STEPS.map((step) => (
        <div key={step.label} className="flex flex-col gap-2">
          <span className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
            {step.label} Step
          </span>
          <div className="flex flex-col gap-2 border-l border-border pl-3">
            {step.activities.map((activity) => (
              <ActivityRow
                key={activity.label}
                activity={activity}
                enabled={!activity.skip || !skips.includes(activity.skip)}
                onChange={(enabled) =>
                  onChange(toggleSkip(skips, activity.skip ?? "", enabled))
                }
              />
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}

// VerifyWarning is what an unticked Verify costs, said the same way wherever the
// picker is shown.
function VerifyWarning({ className }: { className?: string }) {
  return (
    <p className={cn("font-mono text-xs text-warn", className)}>
      Nothing checks this slice before it ships — it needs manual QA.
    </p>
  );
}

// RunOptions folds the picker into an add form, collapsed so an add stays one
// gesture; the chosen set rides with the item it queues. Open is the caller's,
// so a form that seeds a set already stored on the ticket can show it rather
// than hide it behind a closed panel.
export function RunOptions({
  skips,
  onChange,
  open,
  onOpenChange,
  className,
}: {
  skips: string[];
  onChange: (skips: string[]) => void;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  className?: string;
}) {
  return (
    <details
      open={open}
      onToggle={(e) => onOpenChange(e.currentTarget.open)}
      className={cn("group flex flex-col", className)}
    >
      <summary className="flex list-none items-center gap-1.5 font-mono text-xs text-muted-foreground transition-colors hover:text-foreground [&::-webkit-details-marker]:hidden">
        <ChevronRight
          className="size-3.5 shrink-0 transition-transform group-open:rotate-90"
          aria-hidden="true"
        />
        <span>Run options</span>
        {skips.length > 0 && (
          <span className="text-warn">{skipNames(skips)} skipped</span>
        )}
      </summary>
      <div className="mt-2 flex flex-col gap-2 pl-5">
        <p className="font-sans text-xs leading-relaxed text-muted-foreground">
          Every Activity runs unless you untick it. The choice rides with this
          item only.
        </p>
        <PipelineStepList skips={skips} onChange={onChange} />
        {skips.includes(SKIP_VERIFY) && <VerifyWarning />}
      </div>
    </details>
  );
}

// RunStepsDialog is the confirmation every run gesture shows: the whole pipeline
// laid out Activity by Activity under its Step, with the five skippable ones as
// checkboxes. Unticking one bypasses that work on the items this launch covers
// and nothing else — no answer here is ever kept as a preference (ADR 0037).
export function RunStepsDialog({
  target,
  onOpenChange,
  onConfirm,
}: {
  target: RunStepsTarget;
  onOpenChange: (open: boolean) => void;
  onConfirm: (skips: string[]) => void;
}) {
  const [skips, setSkips] = useState<string[]>(() =>
    canonicalSkips(target.skips ?? []),
  );
  const withoutVerify = skips.includes(SKIP_VERIFY);

  return (
    <AlertDialog open onOpenChange={onOpenChange}>
      <AlertDialogContent className="flex max-h-[calc(100dvh-2rem)] flex-col gap-0 overflow-hidden border-border bg-popover p-0 shadow-xl sm:max-w-md">
        <div className="flex shrink-0 items-center gap-3 border-b border-border px-4 py-2.5">
          <div className="flex items-center gap-1.5" aria-hidden="true">
            <span className="size-2.5 rounded-full bg-fail" />
            <span className="size-2.5 rounded-full bg-warn" />
            <span className="size-2.5 rounded-full bg-done" />
          </div>
          <span className="font-mono text-xs text-muted-foreground">
            run-steps
          </span>
        </div>
        <AlertDialogHeader className="shrink-0 gap-2 px-4 pb-2 pt-4 text-left">
          <AlertDialogTitle className="font-mono text-sm font-normal text-foreground">
            {target.title ?? `Run ${target.id}`}
          </AlertDialogTitle>
          <AlertDialogDescription className="font-sans text-sm leading-relaxed text-muted-foreground">
            Every Activity below runs unless you untick it. The choice rides with
            the work you are launching — it is never kept as a preference.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <div className="min-h-0 max-h-[55dvh] flex-1 overflow-y-auto overscroll-contain px-4 pb-2">
          <PipelineStepList skips={skips} onChange={setSkips} />
          {target.note && (
            <p className="mt-3 font-sans text-sm leading-relaxed text-muted-foreground">
              {target.note}
            </p>
          )}
          {withoutVerify && <VerifyWarning className="mt-3" />}
        </div>
        <AlertDialogFooter className="shrink-0 border-t border-border px-4 pb-4 pt-2">
          <AlertDialogCancel className="h-8 gap-1.5 px-3 font-mono text-sm">
            Cancel
          </AlertDialogCancel>
          <Button
            size="sm"
            className={cn(
              "h-8 gap-1.5 px-3 font-mono text-sm",
              withoutVerify && "bg-destructive text-white hover:bg-destructive/90",
            )}
            onClick={() => onConfirm(skips)}
          >
            {withoutVerify ? "Run without verification" : target.confirmLabel}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

// useRunSteps wraps a run gesture in the step picker, the same interception shape
// the hand-back choice uses: request opens the dialog over the target, and the
// confirmed set is handed to onProceed, which owns the launch.
export function useRunSteps(
  onProceed: (target: RunStepsTarget, skips: string[]) => void,
): { request: (target: RunStepsTarget) => void; dialog: ReactNode } {
  const [target, setTarget] = useState<RunStepsTarget | null>(null);

  const dialog = target ? (
    <RunStepsDialog
      key={`${target.repo}:${target.id}`}
      target={target}
      onOpenChange={(open) => {
        if (!open) setTarget(null);
      }}
      onConfirm={(skips) => {
        setTarget(null);
        onProceed(target, skips);
      }}
    />
  ) : null;

  return { request: setTarget, dialog };
}
