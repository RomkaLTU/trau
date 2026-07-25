import { describe, expect, it } from "vitest";

import type { LoopHalt } from "@/lib/loop";

import { haltNotice } from "./loop";

function paused(over: Partial<LoopHalt> = {}): LoopHalt {
  return {
    kind: "paused",
    ticket: "TMS-1243",
    reason: "ticket TMS-1243 stopped during verify",
    ...over,
  };
}

describe("haltNotice", () => {
  it("frames a sub-issue's stop reason against the epic that queued it", () => {
    const notice = haltNotice(
      paused({
        reason: "ticket TMS-1242 stopped during verify",
        subTickets: ["TMS-1241", "TMS-1242"],
      }),
    );

    expect(notice.headline).toBe("paused — TMS-1243");
    expect(notice.attribution).toEqual({
      ticket: "TMS-1242",
      text: "sub-ticket TMS-1242 stopped during verify",
    });
  });

  it("frames a foreign ticket without claiming it is a sub-issue", () => {
    const notice = haltNotice(
      paused({ reason: "ticket TMS-1242 stopped during verify" }),
    );

    expect(notice.headline).toBe("paused — TMS-1243");
    expect(notice.attribution).toEqual({
      ticket: "TMS-1242",
      text: "while working TMS-1242: stopped during verify",
    });
  });

  it("carries a reason whole when it names the ticket mid-sentence", () => {
    const notice = haltNotice(
      paused({ reason: "resume adopted the branch for TMS-1242" }),
    );

    expect(notice.headline).toBe("paused — TMS-1243");
    expect(notice.attribution).toEqual({
      ticket: "TMS-1242",
      text: "while working TMS-1242: resume adopted the branch for TMS-1242",
    });
  });

  it("leaves a reason naming the item itself unchanged", () => {
    const notice = haltNotice(paused());

    expect(notice.headline).toBe(
      "paused — ticket TMS-1243 stopped during verify",
    );
    expect(notice.attribution).toBeUndefined();
  });

  it("leaves a reason naming no ticket unchanged", () => {
    const notice = haltNotice(
      paused({ reason: "the verify checks need a browser" }),
    );

    expect(notice.headline).toBe("paused — the verify checks need a browser");
    expect(notice.attribution).toBeUndefined();
  });

  it("falls back to a bare headline with no reason", () => {
    const notice = haltNotice(paused({ reason: "" }));

    expect(notice.headline).toBe("paused");
    expect(notice.attribution).toBeUndefined();
  });

  it("keeps the dedicated provider notices", () => {
    const reauth = haltNotice(
      paused({ reason: "claude authentication required on TMS-1242" }),
    );
    const window = haltNotice(
      paused({ reason: "claude usage limit reached on TMS-1242" }),
    );

    expect(reauth.headline).toBe("paused — re-authentication needed");
    expect(reauth.attribution).toBeUndefined();
    expect(window.headline).toBe("paused — rate limit reached");
    expect(window.attribution).toBeUndefined();
  });
});
