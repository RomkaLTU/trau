import { cn } from "@/lib/utils";
import { type GrillMode } from "@/lib/grill";

// A badged mode says what the session's outcome will be. Interview is the default and
// carries no badge, the same way an issue-anchored row carries no chip where a draft
// does.
const MODE_BADGE: Partial<
  Record<GrillMode, { label: string; title: string; tone: string }>
> = {
  research: {
    label: "research",
    title: "Research session — the outcome is a findings report",
    tone: "border-info/40 bg-info/10 text-info",
  },
  fix: {
    label: "propose fix",
    title:
      "Propose fix session — the outcome rewrites the ticket for the next attempt",
    tone: "border-warn/40 bg-warn/10 text-warn",
  },
};

// SecondOpinionChip marks a session whose interview ends in a panel decision: the
// challengers each draft their own outcome once the interviewer proposes, and the
// panel then argues it out over challenge rounds. It sits beside the mode badge rather
// than replacing it — the mode is still an interview.
export function SecondOpinionChip({
  challengers,
  className,
}: {
  challengers?: string[];
  className?: string;
}) {
  if (!challengers || challengers.length === 0) return null;

  return (
    <span
      title={`Second opinion — ${challengers.join(" and ")} draft their own outcome beside the interviewer's, then the panel challenges them toward one consensus`}
      className={cn(
        "inline-flex shrink-0 items-center rounded-full border border-teal/40 bg-teal/10 px-1.5 py-0.5 font-mono text-[0.65rem] uppercase tracking-wide text-teal",
        className,
      )}
    >
      second opinion
    </span>
  );
}

export function SessionModeBadge({
  mode,
  className,
}: {
  mode?: GrillMode;
  className?: string;
}) {
  const badge = mode ? MODE_BADGE[mode] : undefined;
  if (!badge) return null;

  return (
    <span
      title={badge.title}
      className={cn(
        "inline-flex shrink-0 items-center rounded-full border px-1.5 py-0.5 font-mono text-[0.65rem] uppercase tracking-wide",
        badge.tone,
        className,
      )}
    >
      {badge.label}
    </span>
  );
}
