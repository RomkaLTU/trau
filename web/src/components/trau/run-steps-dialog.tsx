import { useState, type ReactNode } from "react";
import { Check, Lock } from "lucide-react";

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
  toggleSkip,
  type PipelineActivity,
} from "@/lib/skips";
import { cn } from "@/lib/utils";

// RunStepsTarget is one pending run gesture the picker is standing in front of:
// the repo it lands in, the id it runs, the skip set the item already carries,
// and the label the confirm button wears when nothing is unticked.
export interface RunStepsTarget {
  repo: string;
  id: string;
  skips?: string[];
  confirmLabel: string;
  note?: ReactNode;
}

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

// RunStepsDialog is the confirmation both run gestures show: the whole pipeline
// laid out Activity by Activity under its Step, with the five skippable ones as
// checkboxes. Unticking one bypasses that work for this run and nothing else —
// the choice is never remembered past the launch it confirms.
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
            Run {target.id}
          </AlertDialogTitle>
          <AlertDialogDescription className="font-sans text-sm leading-relaxed text-muted-foreground">
            Every Activity below runs unless you untick it. The choice applies to
            this run only — it is not remembered anywhere else.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <div className="min-h-0 max-h-[55dvh] flex-1 overflow-y-auto overscroll-contain px-4 pb-2">
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
                      enabled={
                        !activity.skip || !skips.includes(activity.skip)
                      }
                      onChange={(enabled) =>
                        setSkips((prev) =>
                          toggleSkip(prev, activity.skip ?? "", enabled),
                        )
                      }
                    />
                  ))}
                </div>
              </div>
            ))}
          </div>
          {target.note && (
            <p className="mt-3 font-sans text-sm leading-relaxed text-muted-foreground">
              {target.note}
            </p>
          )}
          {withoutVerify && (
            <p className="mt-3 font-mono text-xs text-warn">
              Nothing checks this slice before it ships — it needs manual QA.
            </p>
          )}
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
