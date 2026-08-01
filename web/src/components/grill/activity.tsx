import { useState, type ComponentType } from "react";
import {
  Brain,
  Check,
  ChevronDown,
  ChevronRight,
  Wrench,
  X,
  type LucideProps,
} from "lucide-react";

import { useActivityShown } from "@/lib/grill-activity";
import { type GrillActivity } from "@/lib/grill";
import { cn } from "@/lib/utils";

type Tone = "flow" | "info" | "success" | "danger";

const toneText: Record<Tone, string> = {
  flow: "text-muted-foreground",
  info: "text-info",
  success: "text-done",
  danger: "text-fail",
};

interface KindMeta {
  label: string;
  icon: ComponentType<LucideProps>;
  tone: Tone;
}

const KIND_META: Record<string, KindMeta> = {
  tool: { label: "Tool", icon: Wrench, tone: "flow" },
  thinking: { label: "Thinking…", icon: Brain, tone: "info" },
  result: { label: "Done", icon: Check, tone: "success" },
};

function kindMeta(kind: string): KindMeta {
  return KIND_META[kind] ?? { label: kind, icon: Wrench, tone: "flow" };
}

function failed(activity: GrillActivity): boolean {
  return activity.kind === "result" && activity.ok === false;
}

// A tool names itself; everything else reads off its kind.
function activityLabel(activity: GrillActivity, meta: KindMeta): string {
  if (failed(activity)) return "Failed";
  return activity.name || meta.label;
}

// The ring holds more than anyone reads while the turn is still working, so the feed
// shows only its tail rather than growing past the thinking row it hangs under.
const WINDOW = 6;

// ActivityFeed is what the agent has been doing this turn, listed under the thinking
// row: one line per tool it reached for or stretch it spent thinking. It is a progress
// hint, not transcript — the ring empties as soon as the turn's message lands.
export function ActivityFeed({ items }: { items: GrillActivity[] }) {
  const [open, setOpen] = useState(true);
  if (items.length === 0) return null;

  const Chevron = open ? ChevronDown : ChevronRight;
  const shown = open ? items.slice(-WINDOW) : items.slice(-1);

  return (
    <div className="flex flex-col gap-0.5">
      <button
        type="button"
        onClick={() => setOpen(!open)}
        aria-expanded={open}
        className="flex items-center gap-1 self-start font-mono text-[0.65rem] uppercase tracking-wide text-muted-foreground transition-colors hover:text-foreground"
      >
        <Chevron className="size-3" aria-hidden="true" />
        activity
        <span className="tabular-nums">{items.length}</span>
      </button>
      {shown.map((activity) => (
        <ActivityRow key={activity.seq} activity={activity} />
      ))}
    </div>
  );
}

function ActivityRow({ activity }: { activity: GrillActivity }) {
  const meta = kindMeta(activity.kind);
  const Icon = failed(activity) ? X : meta.icon;
  const tone = failed(activity) ? "danger" : meta.tone;

  return (
    <div className="flex min-w-0 items-center gap-1.5 pl-1 text-xs">
      <Icon
        className={cn("size-3 shrink-0", toneText[tone])}
        aria-hidden="true"
      />
      <span className="shrink-0 font-mono text-muted-foreground">
        {activityLabel(activity, meta)}
      </span>
      {activity.detail && (
        <span className="truncate text-muted-foreground/70">
          {activity.detail}
        </span>
      )}
    </div>
  );
}

// ActivitySwitch is the reader's opt-in, on the same terms as the auto-accept switch
// beside it: it belongs to the browser rather than the session, so flipping it here
// settles every thread this browser opens.
export function ActivitySwitch() {
  const [shown, setShown] = useActivityShown();

  return (
    <button
      type="button"
      role="switch"
      aria-checked={shown}
      onClick={() => setShown(!shown)}
      title="Shows what the agent is doing mid-turn — the tools it reaches for, the stretches it spends thinking"
      className={cn(
        "flex h-7 shrink-0 items-center gap-1.5 rounded-md px-1.5 font-mono text-xs transition-colors",
        shown ? "text-foreground" : "text-muted-foreground",
      )}
    >
      <span
        className={cn(
          "flex h-3.5 w-6 items-center rounded-full border p-px transition-colors",
          shown ? "border-primary bg-primary" : "border-border bg-input",
        )}
        aria-hidden="true"
      >
        <span
          className={cn(
            "size-2.5 rounded-full bg-background transition-transform",
            shown && "translate-x-2.5",
          )}
        />
      </span>
      Activity
    </button>
  );
}
