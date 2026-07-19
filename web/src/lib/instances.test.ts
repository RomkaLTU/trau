import { afterEach, describe, expect, it, vi } from "vitest";

import {
  anySyncing,
  healthBlocks,
  healthPill,
  repoHealth,
  syncRepo,
  takeoverRun,
  TakeoverError,
  type RepoFreshness,
  type RepoHealthState,
  type RepoView,
} from "./instances";

function respond(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  } as unknown as Response;
}

function repoView(freshness?: RepoFreshness): RepoView {
  return {
    name: "melga",
    root: "/Users/rd/Projects/melga",
    runs_dir: "/Users/rd/Projects/melga/runs",
    live: false,
    allowed: true,
    registered: true,
    freshness,
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("repoHealth", () => {
  it("reads the state the repos API derived", () => {
    const repo = repoView({
      state: "sync-failed",
      syncing: false,
      last_error: "linear: team not found",
    });

    expect(repoHealth(repo)).toBe("sync-failed");
  });

  it("calls a repo with unreadable freshness unconfigured rather than healthy", () => {
    expect(repoHealth(repoView())).toBe("unconfigured");
  });
});

describe("healthPill", () => {
  const cases: [RepoHealthState, string, string][] = [
    ["ready", "success", "ready"],
    ["syncing", "active", "syncing"],
    ["sync-failed", "fail", "sync failing"],
    ["never-synced", "warn", "never synced"],
    ["unconfigured", "warn", "not configured"],
  ];

  it.each(cases)("renders %s as a %s pill", (health, state, label) => {
    expect(healthPill(health)).toEqual({ state, label });
  });

  it("never renders a repo whose seed sync failed as a success", () => {
    expect(healthPill("sync-failed").state).not.toBe("success");
  });
});

describe("healthBlocks", () => {
  const cases: [RepoHealthState, boolean][] = [
    ["ready", false],
    ["syncing", false],
    ["never-synced", false],
    ["sync-failed", true],
    ["unconfigured", true],
  ];

  it.each(cases)("gates %s as %s", (state, blocked) => {
    expect(healthBlocks(state)).toBe(blocked);
  });

  it("gates a freshly registered repo whose seed sync failed", () => {
    const repo = repoView({
      state: "sync-failed",
      syncing: false,
      last_error: "boom",
    });

    expect(healthBlocks(repoHealth(repo))).toBe(true);
  });
});

describe("anySyncing", () => {
  it("holds the poll open while a repo is still pulling", () => {
    const repos = [
      repoView({
        state: "ready",
        syncing: false,
        last_synced_at: "2026-07-15T09:00:00Z",
      }),
      repoView({ state: "syncing", syncing: true }),
    ];

    expect(anySyncing(repos)).toBe(true);
  });

  it("goes quiet once nothing is syncing", () => {
    const repos = [
      repoView({
        state: "ready",
        syncing: false,
        last_synced_at: "2026-07-15T09:00:00Z",
      }),
      repoView({ state: "sync-failed", syncing: false, last_error: "boom" }),
      repoView({ state: "unconfigured", syncing: false }),
    ];

    expect(anySyncing(repos)).toBe(false);
    expect(anySyncing([])).toBe(false);
  });
});

describe("syncRepo", () => {
  it("surfaces the hub reason a retry failed", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          respond(502, { error: "sync failed: linear: unauthorized" }),
        ),
    );

    const error = await syncRepo("melga").catch((e) => e);

    expect(error.message).toBe("sync failed: linear: unauthorized");
  });
});

describe("takeoverRun", () => {
  it("posts the takeover and returns whether a stop preceded the launch", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(respond(200, { stopped: true, opened: true }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await takeoverRun("melga", "COD-1016");

    expect(result).toEqual({ stopped: true, opened: true });
    expect(fetchMock.mock.calls[0][0]).toBe(
      "/api/v1/repos/melga/runs/COD-1016/takeover",
    );
    expect(fetchMock.mock.calls[0][1].method).toBe("POST");
  });

  it("carries the hub refusal message and status for the toast", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          respond(409, { error: "no resumable claude session for COD-1016" }),
        ),
    );

    const error = await takeoverRun("melga", "COD-1016").catch((e) => e);

    expect(error).toBeInstanceOf(TakeoverError);
    expect(error.message).toBe("no resumable claude session for COD-1016");
    expect(error.status).toBe(409);
  });

  it("marks an unsupported platform by status so the button can hide", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          respond(501, { error: "terminal takeover needs a macOS hub" }),
        ),
    );

    const error = await takeoverRun("melga", "COD-1016").catch((e) => e);

    expect(error).toBeInstanceOf(TakeoverError);
    expect(error.status).toBe(501);
  });
});
