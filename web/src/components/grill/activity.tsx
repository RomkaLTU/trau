import { useState, type ComponentType } from "react";
import {
  Brain,
  Check,
  ChevronDown,
  ChevronRight,
  Loader2,
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

interface RowMark {
  icon: ComponentType<LucideProps>;
  tone: Tone;
  spin?: boolean;
}

// A tool row spins for as long as the call is out, then settles in place rather than
// listing a second row.
function rowMark(activity: GrillActivity): RowMark {
  if (activity.kind === "thinking") return { icon: Brain, tone: "info" };
  if (activity.ok === undefined) return { icon: Loader2, tone: "flow", spin: true };
  return activity.ok
    ? { icon: Check, tone: "success" }
    : { icon: X, tone: "danger" };
}

function rowLabel(activity: GrillActivity): string {
  if (activity.kind === "thinking") return "Thinking…";
  return activity.name || activity.kind;
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
  const { icon: Icon, tone, spin } = rowMark(activity);
  const thinking = activity.kind === "thinking" ? activity.text : undefined;

  return (
    <div className="flex min-w-0 items-center gap-1.5 pl-1 text-xs">
      <Icon
        className={cn("size-3 shrink-0", toneText[tone], spin && "animate-spin")}
        aria-hidden="true"
      />
      {thinking ? (
        <ThinkingLine text={thinking} />
      ) : (
        <>
          <span className="shrink-0 font-mono text-muted-foreground">
            {rowLabel(activity)}
          </span>
          {activity.detail && (
            <span className="truncate text-muted-foreground/70">
              {activity.detail}
            </span>
          )}
        </>
      )}
    </div>
  );
}

// The live edge of a stretch is its tail, so the line is clipped at its start rather
// than its end: a nowrap span pushed to the end of an overflow-hidden box overflows
// backwards, out of reach. The auto margin absorbs the slack until there is none, so a
// stretch that still fits reads from the left like every other row.
function ThinkingLine({ text }: { text: string }) {
  return (
    <span className="flex min-w-0 flex-1 justify-end overflow-hidden">
      <span className="me-auto whitespace-nowrap italic text-muted-foreground/70">
        {text}
      </span>
    </span>
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
