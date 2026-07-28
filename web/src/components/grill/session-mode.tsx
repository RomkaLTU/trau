import { type ModeSuggestion } from "@/components/grill/mode-suggestion";
import { SegmentedControl } from "@/components/trau/segmented-control";
import { cn } from "@/lib/utils";
import { type GrillMode } from "@/lib/grill";

const MODE_OPTIONS: readonly { value: GrillMode; label: string }[] = [
  { value: "interview", label: "Interview" },
  { value: "research", label: "Research" },
];

// SessionTypeChips is the explicit declaration a start surface makes before a
// session exists: an Interview clarifies or authors an issue, Research answers a
// question. The mode locks at the first turn, so it is never offered again once the
// session is running. The hint marks a selection the hub read off the focus note
// rather than one the user made.
export function SessionTypeChips({
  sessionType,
  className,
}: {
  sessionType: ModeSuggestion;
  className?: string;
}) {
  return (
    <span className={cn("inline-flex items-center gap-2", className)}>
      <SegmentedControl
        aria-label="Session type"
        options={MODE_OPTIONS}
        value={sessionType.mode}
        onChange={sessionType.choose}
      />
      {sessionType.suggested && (
        <span
          title="Suggested from your note — pick the other chip to change it"
          className="font-mono text-[0.65rem] uppercase tracking-wide text-muted-foreground"
        >
          suggested
        </span>
      )}
    </span>
  );
}

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
