// @vitest-environment happy-dom
import {
  notifyManager,
  QueryClient,
  QueryClientProvider,
} from "@tanstack/react-query";
import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { GrillListResponse, GrillSession } from "@/lib/grill";

import { proposeFixable, useProposeFix, type ProposeFix } from "./propose-fix";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

notifyManager.setScheduler((cb) => cb());

function session(over: Partial<GrillSession>): GrillSession {
  return {
    id: "1",
    repo: "loop",
    issue_id: "COD-1",
    state: "running",
    created_at: "2026-07-17T10:00:00Z",
    updated_at: "2026-07-17T10:00:00Z",
    ...over,
  };
}

function jsonResponse(status: number, body: unknown): Response {
  return {
    ok: status < 400,
    status,
    json: () => Promise.resolve(body),
  } as Response;
}

let root: Root | undefined;

function renderProposeFix(sessions: GrillSession[]) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  client.setQueryData<GrillListResponse>(["grill", "loop"], {
    repo: "loop",
    sessions,
  });
  const started = vi.fn();
  const result = {} as { current: ProposeFix };
  function Probe() {
    result.current = useProposeFix({
      repo: "loop",
      ticket: "COD-1",
      enabled: true,
      onStarted: started,
    });
    return null;
  }
  const mounted = createRoot(document.createElement("div"));
  root = mounted;
  act(() => {
    mounted.render(
      createElement(QueryClientProvider, { client }, createElement(Probe)),
    );
  });
  return { result, started };
}

afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  vi.unstubAllGlobals();
});

describe("proposeFixable", () => {
  it("offers the act on a dead end or a fault, and on nothing else", () => {
    expect(proposeFixable("gave_up", false)).toBe(true);
    expect(proposeFixable("faulted", false)).toBe(true);
    expect(proposeFixable("paused", false)).toBe(false);
    expect(proposeFixable("stopped", false)).toBe(false);
    expect(proposeFixable(undefined, false)).toBe(false);
  });

  it("withholds it while something is still running the ticket", () => {
    expect(proposeFixable("gave_up", true)).toBe(false);
  });
});

describe("useProposeFix", () => {
  // The dossier is the seed, so the create carries no idea of its own — and it must
  // auto-accept, or the diagnosis would park on its own first recommendation.
  it("opens an auto-accepting fix session with no seed", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse(201, session({ mode: "fix" })));
    vi.stubGlobal("fetch", fetchMock);
    const { result, started } = renderProposeFix([]);

    await act(async () => result.current.start());

    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/repos/loop/grill",
      expect.objectContaining({
        body: JSON.stringify({
          issue_id: "COD-1",
          idea: "",
          model: "",
          provider: "",
          mode: "fix",
          auto_accept: true,
        }),
      }),
    );
    expect(started).toHaveBeenCalledTimes(1);
  });

  // A session that raced this one is the session to join, so the surface still lands
  // on the Inbox rather than reporting a failure.
  it("treats the one-session-per-issue conflict as a start", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(jsonResponse(409, { error: "already active" })),
    );
    const { result, started } = renderProposeFix([]);

    await act(async () => result.current.start());

    expect(started).toHaveBeenCalledTimes(1);
    expect(result.current.error).toBe("");
  });

  it("surfaces a real refusal instead of navigating", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          jsonResponse(400, { error: "COD-1 is no longer in a failed state" }),
        ),
    );
    const { result, started } = renderProposeFix([]);

    await act(async () => result.current.start());

    expect(started).not.toHaveBeenCalled();
    expect(result.current.error).toBe("COD-1 is no longer in a failed state");
  });

  // A ticket already being diagnosed is linked to, never started a second time.
  it("reports the ticket's live fix session, ignoring its interview", () => {
    const { result } = renderProposeFix([
      session({ id: "9", mode: "interview" }),
      session({ id: "7", mode: "fix" }),
    ]);
    expect(result.current.session?.id).toBe("7");
  });
});
