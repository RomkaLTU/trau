import { beforeEach, describe, expect, it, vi } from "vitest";

import { apiFetch } from "./api";
import {
  handbackChoices,
  handbackStep,
  pendingHandback,
  settleHandback,
  type Handback,
} from "./handback";

vi.mock("./api", () => ({ apiFetch: vi.fn() }));

const mockFetch = vi.mocked(apiFetch);

function stamped(over: Partial<Handback> = {}): Handback {
  return { at: "2026-07-22T10:00:00Z", phase: "build", advance: "built", ...over };
}

function response(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as Response;
}

beforeEach(() => {
  mockFetch.mockReset();
});

describe("pendingHandback", () => {
  it("finds the stamped run and ignores the rest", () => {
    const runs = [
      { ticket: "COD-1", handback: stamped() },
      { ticket: "COD-2" },
    ];
    expect(pendingHandback(runs, "COD-1")).toEqual(stamped());
    expect(pendingHandback(runs, "COD-2")).toBeNull();
    expect(pendingHandback(runs, "COD-9")).toBeNull();
    expect(pendingHandback(undefined, "COD-1")).toBeNull();
  });
});

describe("handbackChoices", () => {
  it("names the interrupted phase in both options", () => {
    expect(handbackChoices(stamped())).toEqual({
      rerun: "Re-run build",
      advance: "Build is done — continue to the next step",
    });
  });

  it("drops the advance option when the phase has no completed value", () => {
    expect(handbackChoices(stamped({ phase: "commit", advance: undefined }))).toEqual({
      rerun: "Re-run commit",
      advance: null,
    });
  });

  it("falls back to a generic name when the stamp has no session phase", () => {
    expect(handbackChoices(stamped({ phase: "" }))).toEqual({
      rerun: "Re-run the interrupted step",
      advance: "The interrupted step is done — continue to the next step",
    });
  });
});

describe("handbackStep", () => {
  it("phrases the interrupted phase, named or not", () => {
    expect(handbackStep(stamped())).toBe("the build step");
    expect(handbackStep(stamped({ phase: "" }))).toBe("the interrupted step");
  });
});

describe("settleHandback", () => {
  it("posts the chosen branch to the run's advance endpoint", async () => {
    mockFetch.mockResolvedValue(
      response(200, { ticket: "COD-1", from: "building", phase: "built" }),
    );

    await settleHandback("acme", "COD-1", false);

    const [url, init] = mockFetch.mock.calls[0]
    expect(url).toBe("/api/v1/repos/acme/runs/COD-1/advance");
    expect(init?.method).toBe("POST");
    expect(JSON.parse(String(init?.body))).toEqual({ rerun: false });

    await settleHandback("acme", "COD-1", true);
    expect(JSON.parse(String(mockFetch.mock.calls[1][1]?.body))).toEqual({ rerun: true });
  });

  it("surfaces the hub's refusal", async () => {
    mockFetch.mockResolvedValue(response(409, { error: "not steered", reason: "no_takeover" }));

    await expect(settleHandback("acme", "COD-1", false)).rejects.toThrow("not steered");
  });
});
