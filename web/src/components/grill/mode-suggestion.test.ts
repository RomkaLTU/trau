// @vitest-environment happy-dom
import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useModeSuggestion, type ModeSuggestion } from "./mode-suggestion";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

function modeResponse(mode: string, suggested = true): Response {
  return {
    ok: true,
    status: 200,
    json: () => Promise.resolve({ mode, suggested }),
  } as Response;
}

let root: Root | undefined;

function renderSuggestion() {
  const result = {} as { current: ModeSuggestion };
  function Probe({ repo }: { repo: string }) {
    result.current = useModeSuggestion(repo);
    return null;
  }
  const mounted = createRoot(document.createElement("div"));
  root = mounted;
  const show = (repo: string) =>
    act(() => mounted.render(createElement(Probe, { repo })));
  show("loop");
  return { result, switchRepo: show };
}

// settle lets the suggestion's promise chain resolve without advancing the debounce.
async function settle() {
  await act(async () => {});
}

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
});

afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("useModeSuggestion", () => {
  it("pre-selects the suggested mode once typing pauses", async () => {
    const fetchMock = vi.fn().mockResolvedValue(modeResponse("research"));
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderSuggestion();

    act(() => result.current.noteChanged("compare the oauth libraries"));
    expect(fetchMock).not.toHaveBeenCalled();

    await act(async () => vi.advanceTimersByTime(600));
    await settle();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/repos/loop/grill/suggest-mode");
    expect(JSON.parse(init.body as string)).toEqual({
      text: "compare the oauth libraries",
    });
    expect(result.current.mode).toBe("research");
    expect(result.current.suggested).toBe(true);
  });

  it("asks at once when the note loses focus", async () => {
    const fetchMock = vi.fn().mockResolvedValue(modeResponse("research"));
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderSuggestion();

    await act(async () => result.current.noteSettled("  compare vendors  "));
    await settle();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(JSON.parse(init.body as string).text).toBe("compare vendors");
    expect(result.current.mode).toBe("research");

    await act(async () => result.current.noteSettled("compare vendors"));
    await settle();

    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  // The whole point of a visible pre-selection: a chip the user flipped is their
  // declaration, so neither a response still in flight nor any later note may move it.
  it("never overrides a mode the user picked", async () => {
    let answer: (res: Response) => void = () => {};
    const fetchMock = vi
      .fn()
      .mockReturnValue(new Promise<Response>((resolve) => (answer = resolve)));
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderSuggestion();

    await act(async () => result.current.noteSettled("compare the libraries"));
    act(() => result.current.choose("interview"));

    await act(async () => answer(modeResponse("research")));
    await settle();

    expect(result.current.mode).toBe("interview");
    expect(result.current.suggested).toBe(false);

    act(() => result.current.noteChanged("compare even more libraries"));
    await act(async () => vi.advanceTimersByTime(600));
    await settle();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(result.current.mode).toBe("interview");
  });

  it("keeps one request in flight and catches up with the latest note", async () => {
    const answers: ((res: Response) => void)[] = [];
    const fetchMock = vi
      .fn()
      .mockImplementation(
        () => new Promise<Response>((resolve) => answers.push(resolve)),
      );
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderSuggestion();

    await act(async () => result.current.noteSettled("first"));
    await act(async () => result.current.noteSettled("second"));
    await act(async () => result.current.noteSettled("third"));
    expect(fetchMock).toHaveBeenCalledTimes(1);

    await act(async () => answers[0](modeResponse("interview")));
    await settle();

    expect(fetchMock).toHaveBeenCalledTimes(2);
    const [, init] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(JSON.parse(init.body as string).text).toBe("third");
  });

  it("leaves the default standing when the hub cannot be asked", async () => {
    const fetchMock = vi.fn().mockRejectedValue(new Error("offline"));
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderSuggestion();

    await act(async () => result.current.noteSettled("compare the libraries"));
    await settle();

    expect(result.current.mode).toBe("interview");
    expect(result.current.suggested).toBe(false);
  });

  // A hub that fell back to its own default answers with the same mode a real reading
  // would, so the hint must follow the hub's word rather than the mode.
  it("shows no hint when the hub did not classify the note", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(modeResponse("interview", false));
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderSuggestion();

    await act(async () => result.current.noteSettled("compare the libraries"));
    await settle();

    expect(result.current.mode).toBe("interview");
    expect(result.current.suggested).toBe(false);
  });

  // The hint says the chip was read off the note, so a note that no longer exists
  // takes both the hint and the mode it produced with it.
  it("withdraws the suggestion when the note is deleted", async () => {
    const fetchMock = vi.fn().mockResolvedValue(modeResponse("research"));
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderSuggestion();

    await act(async () => result.current.noteSettled("compare the libraries"));
    await settle();
    expect(result.current.mode).toBe("research");

    await act(async () => result.current.noteSettled(""));

    expect(result.current.mode).toBe("interview");
    expect(result.current.suggested).toBe(false);
  });

  it("drops the suggestion when the panel switches repo", async () => {
    const fetchMock = vi.fn().mockResolvedValue(modeResponse("research"));
    vi.stubGlobal("fetch", fetchMock);
    const { result, switchRepo } = renderSuggestion();

    await act(async () => result.current.noteSettled("compare the libraries"));
    await settle();
    expect(result.current.mode).toBe("research");

    switchRepo("m4c-api");

    expect(result.current.mode).toBe("interview");
    expect(result.current.suggested).toBe(false);

    await act(async () => result.current.noteSettled("compare the libraries"));
    await settle();

    expect(fetchMock).toHaveBeenCalledTimes(2);
    const [url] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(url).toBe("/api/v1/repos/m4c-api/grill/suggest-mode");
    expect(result.current.mode).toBe("research");
  });

  // A pick is the user's declaration rather than a reading of one repo's note, so the
  // switch that drops the suggestion leaves the pick standing.
  it("keeps a mode the user picked across a repo switch", async () => {
    const fetchMock = vi.fn().mockResolvedValue(modeResponse("interview"));
    vi.stubGlobal("fetch", fetchMock);
    const { result, switchRepo } = renderSuggestion();

    act(() => result.current.choose("research"));
    switchRepo("m4c-api");

    expect(result.current.mode).toBe("research");
    expect(result.current.suggested).toBe(false);
  });
});
