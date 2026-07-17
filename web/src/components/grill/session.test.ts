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

import { useGrillSession, type GrillSessionState } from "./session";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

// Query notifications default to a setTimeout batch, which would land after an
// awaited act; delivering them synchronously keeps every assertion deterministic.
notifyManager.setScheduler((cb) => cb());

function session(over: Partial<GrillSession>): GrillSession {
  return {
    id: "1",
    repo: "loop",
    issue_id: "COD-1",
    state: "waiting",
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

// renderGrillSession mounts the hook on a seeded list cache, so no test depends on
// the list poll: the only fetches are the ones the act under test issues.
function renderGrillSession(sessions: GrillSession[]) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  client.setQueryData<GrillListResponse>(["grill", "loop"], {
    repo: "loop",
    sessions,
  });
  const result = {} as { current: GrillSessionState };
  function Probe() {
    result.current = useGrillSession("loop", "COD-1");
    return null;
  }
  const mounted = createRoot(document.createElement("div"));
  root = mounted;
  act(() => {
    mounted.render(
      createElement(QueryClientProvider, { client }, createElement(Probe)),
    );
  });
  return { client, result };
}

afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  vi.unstubAllGlobals();
});

describe("useGrillSession end", () => {
  it("no-ops without a live session to settle", () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderGrillSession([session({ state: "applied" })]);

    act(() => result.current.end());

    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("no-ops while an abandon is in flight", async () => {
    const fetchMock = vi.fn().mockReturnValue(new Promise(() => {}));
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderGrillSession([session({})]);

    await act(async () => result.current.end());
    expect(result.current.ending).toBe(true);
    act(() => result.current.end());

    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("optimistically settles the issue's sessions and confirms through onEnded", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse(200, session({ state: "abandoned" })));
    vi.stubGlobal("fetch", fetchMock);
    const { client, result } = renderGrillSession([
      session({}),
      session({ id: "2", issue_id: "COD-2" }),
    ]);
    const onEnded = vi.fn();

    await act(async () => result.current.end(onEnded));

    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/grill/1/abandon",
      expect.objectContaining({ method: "POST" }),
    );
    const list = client.getQueryData<GrillListResponse>(["grill", "loop"]);
    expect(list?.sessions.map((s) => s.state)).toEqual([
      "abandoned",
      "waiting",
    ]);
    expect(result.current.session).toBeUndefined();
    expect(onEnded).toHaveBeenCalledOnce();
  });

  it("surfaces a refused abandon as endError and skips onEnded", async () => {
    const fetchMock = vi.fn((input: string) =>
      Promise.resolve(
        input.endsWith("/abandon")
          ? jsonResponse(409, { error: "session is already applied" })
          : jsonResponse(200, {
              repo: "loop",
              sessions: [session({ state: "applied" })],
            }),
      ),
    );
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderGrillSession([session({})]);
    const onEnded = vi.fn();

    await act(async () => result.current.end(onEnded));

    expect(result.current.endError?.message).toBe("session is already applied");
    expect(onEnded).not.toHaveBeenCalled();
  });
});
