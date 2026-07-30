import { Check } from "lucide-react";

import { cn } from "@/lib/utils";

// AutoAcceptToggle is the opt-in a start surface offers before a session exists: the
// agent's own recommendation answers the question it came with, so only the choices
// needing the user's taste are ever put to them. It settles the session's opening
// value; AutoAcceptSwitch below is the same choice once the interview is under way.
export function AutoAcceptToggle({
  checked,
  onChange,
  className,
}: {
  checked: boolean;
  onChange: (checked: boolean) => void;
  className?: string;
}) {
  return (
    <button
      type="button"
      role="checkbox"
      aria-checked={checked}
      onClick={() => onChange(!checked)}
      className={cn("flex items-start gap-2 text-left", className)}
    >
      <span
        className={cn(
          "mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-sm border transition-colors",
          checked
            ? "border-primary bg-primary text-primary-foreground"
            : "border-border bg-input",
        )}
        aria-hidden="true"
      >
        {checked && <Check className="size-3" />}
      </span>
      <span className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
        <span className="font-mono text-xs text-foreground">
          Auto-accept recommendations
        </span>
        <span className="text-xs text-muted-foreground">
          asks you only when human taste is required
        </span>
      </span>
    </button>
  );
}

// AutoAcceptSwitch is the same opt-in on a live session, sitting beside the model
// select on the same terms: it applies from the next question, and switching it on
// while a recommended question is already waiting answers that one too.
export function AutoAcceptSwitch({
  checked,
  disabled,
  onChange,
}: {
  checked: boolean;
  disabled?: boolean;
  onChange: (checked: boolean) => void;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      title="Answers with the interviewer's own recommendation, asking you only when human taste is required"
      className={cn(
        "flex h-7 shrink-0 items-center gap-1.5 rounded-md px-1.5 font-mono text-xs transition-colors disabled:pointer-events-none disabled:opacity-50",
        checked ? "text-foreground" : "text-muted-foreground",
      )}
    >
      <span
        className={cn(
          "flex h-3.5 w-6 items-center rounded-full border p-px transition-colors",
          checked ? "border-primary bg-primary" : "border-border bg-input",
        )}
        aria-hidden="true"
      >
        <span
          className={cn(
            "size-2.5 rounded-full bg-background transition-transform",
            checked && "translate-x-2.5",
          )}
        />
      </span>
      Auto-accept
    </button>
  );
}

// AutoAcceptBadge marks an answer the hub took from the agent's recommendation rather
// than from the user, so a transcript full of instant answers still shows who chose.
export function AutoAcceptBadge({ className }: { className?: string }) {
  return (
    <span
      title="Auto-accepted — the interviewer's own recommendation answered this"
      className={cn(
        "inline-flex shrink-0 items-center rounded-full border border-border bg-secondary/60 px-1.5 py-0.5 font-mono text-[0.65rem] uppercase tracking-wide text-muted-foreground",
        className,
      )}
    >
      auto-accepted
    </span>
  );
}
