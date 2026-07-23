import { afterEach, describe, expect, it, vi } from "vitest";

import {
  CheckpointError,
  checkpointErrorText,
  clearRun,
  reconcileRepo,
  resetRun,
} from "./checkpoints";
import { TAKEOVER_BLOCKED } from "./instances";

function respond(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  } as unknown as Response;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("checkpoint mutations under a takeover", () => {
  const takeoverConflict = {
    error:
      "melga is taken over — PID 4242 holds COD-7 in a terminal session; close it first",
    reason: "taken_over",
    live: true,
  };

  const cases: [string, () => Promise<unknown>][] = [
    ["reset", () => resetRun("melga", "COD-7", false)],
    ["clear", () => clearRun("melga", "COD-7")],
    ["reconcile", () => reconcileRepo("melga")],
  ];

  it.each(cases)("flags the %s conflict as a takeover", async (_name, call) => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(respond(409, takeoverConflict)),
    );

    const error = await call().catch((e) => e);

    expect(error).toBeInstanceOf(CheckpointError);
    expect((error as CheckpointError).takenOver).toBe(true);
    expect(checkpointErrorText(error)).toBe(TAKEOVER_BLOCKED);
  });

  it("leaves a live-loop conflict reading as the hub wrote it", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        respond(409, {
          error: "melga has a live loop (pid 99) — stop it before changing its checkpoints",
          live: true,
        }),
      ),
    );

    const error = await resetRun("melga", "COD-7", false).catch((e) => e);

    expect((error as CheckpointError).takenOver).toBe(false);
    expect((error as CheckpointError).live).toBe(true);
    expect(checkpointErrorText(error)).toContain("live loop");
  });

  it("still escalates a merged ticket to a forced reset", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        respond(409, {
          error: "COD-7 is already merged",
          requires_force: true,
        }),
      ),
    );

    const error = await resetRun("melga", "COD-7", false).catch((e) => e);

    expect((error as CheckpointError).requiresForce).toBe(true);
    expect((error as CheckpointError).takenOver).toBe(false);
  });
});
