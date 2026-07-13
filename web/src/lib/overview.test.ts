import { describe, expect, it } from "vitest";

import {
  activeLoopCount,
  isActiveState,
  loopCardView,
  phasePill,
  phaseRank,
  repoBadgeState,
  sessionStatePill,
  type LiveLoop,
  type SessionState,
} from "@/lib/overview";

describe("phaseRank", () => {
  it("ranks the checkpoint pipeline in order", () => {
    expect(phaseRank("building")).toBe(1);
    expect(phaseRank("built")).toBe(2);
    expect(phaseRank("handed_off")).toBe(3);
    expect(phaseRank("verified")).toBe(4);
    expect(phaseRank("pr_open")).toBe(5);
    expect(phaseRank("merged")).toBe(6);
  });

  it("treats unknown/empty phases as rank 0", () => {
    expect(phaseRank("")).toBe(0);
    expect(phaseRank("quarantined")).toBe(0);
  });
});

describe("phasePill", () => {
  it("maps checkpoint phases to run-state pills", () => {
    expect(phasePill("building")).toEqual({ state: "active", label: "build" });
    expect(phasePill("handed_off")).toEqual({
      state: "active",
      label: "handoff",
    });
    expect(phasePill("verified")).toEqual({ state: "verify", label: "verify" });
    expect(phasePill("pr_open")).toEqual({ state: "info", label: "pr" });
    expect(phasePill("merged")).toEqual({ state: "success", label: "merged" });
  });

  it("falls back to the raw phase for anything unmapped", () => {
    expect(phasePill("picking")).toEqual({ state: "active", label: "picking" });
    expect(phasePill("")).toEqual({ state: "active", label: "running" });
  });
});

function loop(sessionState: SessionState): LiveLoop {
  return { repo: "trucknet", pid: 1, sessionState, phase: "", startedAt: "" };
}

describe("active loop counting", () => {
  it("counts only grazing, working, and stopping as active", () => {
    expect(isActiveState("grazing")).toBe(true);
    expect(isActiveState("working")).toBe(true);
    expect(isActiveState("stopping")).toBe(true);
    expect(isActiveState("parked")).toBe(false);
    expect(isActiveState("idle")).toBe(false);
    expect(isActiveState("unknown")).toBe(false);
  });

  it("excludes parked, idle, and unknown loops from the tile count", () => {
    const loops = [
      loop("working"),
      loop("grazing"),
      loop("stopping"),
      loop("parked"),
      loop("idle"),
      loop("unknown"),
    ];
    expect(activeLoopCount(loops)).toBe(3);
  });

  it("reads zero active loops when only a parked instance exists", () => {
    expect(activeLoopCount([loop("parked")])).toBe(0);
  });
});

describe("loopCardView", () => {
  it("labels the working pill by the corrected Step, not the checkpoint rank", () => {
    const view = loopCardView("working", { phase: "handed_off" });
    expect(view.pill).toEqual({ state: "active", label: "verify" });
    expect(view.showStepper).toBe(true);
    expect(view.showWatch).toBe(true);
    expect(view.showStop).toBe(true);
    expect(view.stopDisabled).toBe(false);
  });

  it("prefers the live Activity over the checkpoint for the working pill", () => {
    expect(loopCardView("working", { phase: "handed_off", activity: "ci-wait" }).pill).toEqual({
      state: "active",
      label: "ship",
    });
  });

  it("maps a parked failure class to the attention pill with Watch + Stop", () => {
    expect(loopCardView("parked", { failureClass: "paused" }).pill).toEqual({
      state: "warn",
      label: "paused",
    });
    const faulted = loopCardView("parked", { failureClass: "faulted" });
    expect(faulted.pill).toEqual({ state: "fail", label: "fault" });
    expect(faulted.showStepper).toBe(false);
    expect(faulted.showWatch).toBe(true);
    expect(faulted.showStop).toBe(true);
    expect(faulted.copy).toMatch(/parked on the recap/i);
  });

  it("grazes without a ticket, watch, or stepper", () => {
    const view = loopCardView("grazing");
    expect(view.pill).toEqual({ state: "active", label: "grazing" });
    expect(view.showWatch).toBe(false);
    expect(view.showStepper).toBe(false);
    expect(view.showStop).toBe(true);
  });

  it("dims idle and offers Stop only", () => {
    const view = loopCardView("idle");
    expect(view.dimmed).toBe(true);
    expect(view.showWatch).toBe(false);
    expect(view.showStop).toBe(true);
    expect(view.copy).toMatch(/nothing live/i);
  });

  it("disables actions while stopping", () => {
    const view = loopCardView("stopping");
    expect(view.pill.label).toBe("stopping…");
    expect(view.stopDisabled).toBe(true);
    expect(view.showStop).toBe(true);
  });

  it("still offers Stop on an unknown, pre-reporting binary", () => {
    const view = loopCardView("unknown");
    expect(view.pill).toEqual({ state: "todo", label: "unknown" });
    expect(view.showStop).toBe(true);
    expect(view.copy).toMatch(/predates session reporting/i);
  });
});

describe("sessionStatePill", () => {
  it("keeps working and grazing on the teal active palette", () => {
    expect(sessionStatePill("working")).toEqual({ state: "active", label: "working" });
    expect(sessionStatePill("grazing")).toEqual({ state: "active", label: "grazing" });
  });

  it("reads parked as a warn, needs-you pill", () => {
    expect(sessionStatePill("parked")).toEqual({ state: "warn", label: "parked" });
  });

  it("dims idle and unknown", () => {
    expect(sessionStatePill("idle")).toEqual({ state: "todo", label: "idle" });
    expect(sessionStatePill("unknown")).toEqual({ state: "todo", label: "unknown" });
  });
});

describe("repoBadgeState", () => {
  it("has no badge without a live instance", () => {
    expect(repoBadgeState([])).toBe("none");
  });

  it("reads any active instance as active", () => {
    expect(repoBadgeState(["parked", "working"])).toBe("active");
    expect(repoBadgeState(["grazing"])).toBe("active");
    expect(repoBadgeState(["stopping"])).toBe("active");
  });

  it("reads a repo whose only instance is parked as needs-you, not busy", () => {
    expect(repoBadgeState(["parked"])).toBe("parked");
    expect(repoBadgeState(["idle", "parked"])).toBe("parked");
  });

  it("dims a repo with only idle or unknown instances", () => {
    expect(repoBadgeState(["idle"])).toBe("idle");
    expect(repoBadgeState(["unknown"])).toBe("idle");
  });
});
