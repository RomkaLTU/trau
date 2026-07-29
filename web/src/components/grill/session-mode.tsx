import { cn } from "@/lib/utils";
import { type GrillMode } from "@/lib/grill";

// SessionModeBadge marks a research session wherever sessions are listed. Interview
// is the default and carries no badge, the same way an issue-anchored row carries no
// chip where a draft does.
export function SessionModeBadge({
  mode,
  className,
}: {
  mode?: GrillMode;
  className?: string;
}) {
  if (mode !== "research") return null;

  return (
    <span
      title="Research session — the outcome is a findings report"
      className={cn(
        "inline-flex shrink-0 items-center rounded-full border border-info/40 bg-info/10 px-1.5 py-0.5 font-mono text-[0.65rem] uppercase tracking-wide text-info",
        className,
      )}
    >
      research
    </span>
  );
}
