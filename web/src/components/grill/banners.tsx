import {
  AlertTriangle,
  CheckCircle2,
  Loader2,
  PauseCircle,
  Sparkles,
  XCircle,
  type LucideIcon,
} from "lucide-react";

import type { RunState } from "@/components/trau";
import type { GrillBanner, GrillBannerTone, GrillState } from "@/lib/grill";
import { cn } from "@/lib/utils";

const BANNER_STYLES: Record<
  GrillBannerTone,
  { className: string; icon: LucideIcon; spin?: boolean }
> = {
  thinking: {
    className: "border-teal/40 bg-teal/5 text-foreground",
    icon: Loader2,
    spin: true,
  },
  parked: {
    className: "border-border bg-muted/40 text-foreground",
    icon: PauseCircle,
  },
  stalled: {
    className: "border-warn/40 bg-warn/5 text-foreground",
    icon: AlertTriangle,
  },
  finished: {
    className: "border-info/40 bg-info/5 text-foreground",
    icon: Sparkles,
  },
  applied: {
    className: "border-done/40 bg-done/5 text-foreground",
    icon: CheckCircle2,
  },
  ended: {
    className: "border-border bg-muted/40 text-muted-foreground",
    icon: XCircle,
  },
};

export function BannerRow({ banner }: { banner: GrillBanner }) {
  const style = BANNER_STYLES[banner.tone];
  const Icon = style.icon;
  return (
    <div
      className={cn(
        "flex items-start gap-2.5 rounded-md border px-3 py-2.5",
        style.className,
      )}
    >
      <Icon
        className={cn("mt-0.5 size-4 shrink-0", style.spin && "animate-spin")}
        aria-hidden="true"
      />
      <div className="flex flex-col gap-0.5">
        <p className="text-sm font-medium">{banner.headline}</p>
        {banner.hint && (
          <p className="text-xs leading-relaxed text-muted-foreground">
            {banner.hint}
          </p>
        )}
      </div>
    </div>
  );
}

export function ErrorNote({ message }: { message: string }) {
  return (
    <div className="flex items-start gap-2.5 rounded-md border border-fail/40 bg-fail/5 px-3 py-3">
      <AlertTriangle
        className="mt-0.5 size-3.5 shrink-0 text-fail"
        aria-hidden="true"
      />
      <p className="text-xs leading-relaxed text-muted-foreground">{message}</p>
    </div>
  );
}

const STATE_PILLS: Record<GrillState, { state: RunState; label: string }> = {
  running: { state: "active", label: "thinking" },
  waiting: { state: "info", label: "your turn" },
  parked: { state: "todo", label: "parked" },
  stalled: { state: "warn", label: "stalled" },
  finished: { state: "verify", label: "proposal ready" },
  applied: { state: "success", label: "applied" },
  abandoned: { state: "todo", label: "ended" },
};

export function statePill(state: GrillState): {
  state: RunState;
  label: string;
} {
  return STATE_PILLS[state];
}
