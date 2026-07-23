// @vitest-environment happy-dom
import {
  notifyManager,
  QueryClient,
  QueryClientProvider,
} from "@tanstack/react-query";
import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { apiFetch } from "@/lib/api";
import type { Handback } from "@/lib/handback";

import { useHandback } from "./handback-dialog";

vi.mock("@/lib/api", () => ({ apiFetch: vi.fn() }));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

notifyManager.setScheduler((cb) => cb());

const mockFetch = vi.mocked(apiFetch);

function stamped(over: Partial<Handback> = {}): Handback {
  return { at: "2026-07-22T10:00:00Z", phase: "build", advance: "built", ...over };
}

let root: Root | undefined;

// renderHandback mounts the hook behind a button that requests the hand-back, so
// the assertions run against the rendered dialog rather than the hook's internals.
function renderHandback(handback: Handback | null) {
  const proceeded: string[] = [];
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  function Probe() {
    const choice = useHandback("acme", (ticket) => proceeded.push(ticket));
    return createElement(
      "div",
      null,
      createElement(
        "button",
        { onClick: () => choice.request("COD-1", handback) },
        "Resume",
      ),
      choice.dialog,
    );
  }
  const container = document.createElement("div");
  document.body.appendChild(container);
  const mounted = createRoot(container);
  root = mounted;
  act(() => {
    mounted.render(
      createElement(QueryClientProvider, { client }, createElement(Probe)),
    );
  });
  return { proceeded, container };
}

function click(label: string) {
  const button = [...document.body.querySelectorAll("button")].find(
    (b) => b.textContent?.trim() === label,
  );
  if (!button) throw new Error(`no button labelled ${label}`);
  act(() => button.click());
}

function buttonLabels(): string[] {
  return [...document.body.querySelectorAll("button")].map(
    (b) => b.textContent?.trim() ?? "",
  );
}

beforeEach(() => {
  mockFetch.mockReset();
  mockFetch.mockResolvedValue({
    ok: true,
    status: 200,
    json: () => Promise.resolve({ ticket: "COD-1", from: "building", phase: "built" }),
  } as Response);
});

afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  document.body.innerHTML = "";
});

describe("useHandback", () => {
  it("hands back straight away when no terminal steered the ticket", () => {
    const { proceeded } = renderHandback(null);

    click("Resume");

    expect(proceeded).toEqual(["COD-1"]);
    expect(buttonLabels()).toEqual(["Resume"]);
  });

  it("asks before handing back a ticket a terminal steered", () => {
    const { proceeded } = renderHandback(stamped());

    click("Resume");

    expect(proceeded).toEqual([]);
    expect(buttonLabels()).toContain("Re-run build");
    expect(buttonLabels()).toContain("Build is done — continue to the next step");
  });

  it("offers only a re-run when the phase has no completed value", () => {
    renderHandback(stamped({ phase: "commit", advance: undefined }));

    click("Resume");

    expect(buttonLabels()).toContain("Re-run commit");
    expect(buttonLabels().some((l) => l.includes("continue to the next step"))).toBe(
      false,
    );
  });

  it("names the step generically when the stamp has no session phase", () => {
    renderHandback(stamped({ phase: "" }));

    click("Resume");

    expect(buttonLabels()).toContain("Re-run the interrupted step");
    expect(buttonLabels()).toContain(
      "The interrupted step is done — continue to the next step",
    );
    expect(document.body.textContent).toContain(
      "how far the interrupted step got",
    );
  });

  it("settles the stamp before the ticket re-enters the loop", async () => {
    const { proceeded } = renderHandback(stamped());

    click("Resume");
    click("Build is done — continue to the next step");
    await act(async () => {});

    expect(mockFetch).toHaveBeenCalledWith(
      "/api/v1/repos/acme/runs/COD-1/advance",
      expect.objectContaining({ method: "POST" }),
    );
    expect(JSON.parse(String(mockFetch.mock.calls[0][1]?.body))).toEqual({
      rerun: false,
    });
    expect(proceeded).toEqual(["COD-1"]);
  });

  it("keeps the phase where it is on the re-run branch", async () => {
    const { proceeded } = renderHandback(stamped());

    click("Resume");
    click("Re-run build");
    await act(async () => {});

    expect(JSON.parse(String(mockFetch.mock.calls[0][1]?.body))).toEqual({
      rerun: true,
    });
    expect(proceeded).toEqual(["COD-1"]);
  });

  it("does not hand back when the hub refuses the choice", async () => {
    mockFetch.mockResolvedValue({
      ok: false,
      status: 409,
      json: () => Promise.resolve({ error: "not steered", reason: "no_takeover" }),
    } as Response);
    const { proceeded } = renderHandback(stamped());

    click("Resume");
    click("Re-run build");
    await act(async () => {});

    expect(proceeded).toEqual([]);
    expect(document.body.textContent).toContain("not steered");
  });
});
