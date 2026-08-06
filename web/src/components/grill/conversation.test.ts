// @vitest-environment happy-dom
import {
  notifyManager,
  QueryClient,
  QueryClientProvider,
} from "@tanstack/react-query";
import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { GrillDetail, GrillMessage, GrillSession } from "@/lib/grill";

import { GrillConversation } from "./conversation";

vi.mock("@/lib/sse", () => ({ streamSSE: () => () => {} }));
vi.mock("@/lib/attachments", () => ({ uploadAttachments: vi.fn() }));
vi.mock("sonner", () => ({
  toast: { loading: vi.fn(() => "toast"), dismiss: vi.fn(), error: vi.fn() },
}));
// The composer's editor is tiptap; a textarea carrying the same placeholder and
// disabled flag is all these tests read off it.
vi.mock("@/components/markdown-editor", async () => {
  const { createElement: h } = await import("react");
  return {
    MarkdownEditor: ({
      placeholder,
      disabled,
    }: {
      placeholder: string;
      disabled?: boolean;
    }) =>
      h("textarea", {
        placeholder,
        disabled: disabled ?? false,
        readOnly: true,
      }),
  };
});

(
  globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true;

notifyManager.setScheduler((cb) => cb());

const stalled: GrillSession = {
  id: "1",
  repo: "loop",
  issue_id: "COD-42",
  state: "stalled",
  parked_reason: "usage limit reached",
  created_at: "2026-07-17T10:00:00Z",
  updated_at: "2026-07-17T10:00:00Z",
};

const round: GrillMessage = {
  id: "7",
  role: "agent",
  kind: "question",
  payload: {
    text: "1. Which page is in scope?\n2. Which auth flow?",
    round: [
      { text: "Which page is in scope?", options: ["login", "signup"] },
      { text: "Which auth flow?", options: [] },
    ],
  },
  created_at: "2026-07-17T10:01:00Z",
};

let root: Root | undefined;
let host: HTMLElement | undefined;

async function render(session: GrillSession, messages: GrillMessage[]) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  client.setQueryData<GrillDetail>(["grill-session", session.id], {
    session,
    messages,
  });
  host = document.createElement("div");
  document.body.appendChild(host);
  const mounted = createRoot(host);
  root = mounted;
  await act(async () => {
    mounted.render(
      createElement(
        QueryClientProvider,
        { client },
        createElement(GrillConversation, { repo: "loop", initial: session }),
      ),
    );
  });
  return host;
}

function resumeButton(): HTMLButtonElement | undefined {
  return [...(host?.querySelectorAll("button") ?? [])].find((b) =>
    b.textContent?.includes("Resume session"),
  );
}

function composer(): HTMLTextAreaElement {
  const found = host?.querySelector("textarea[readonly]");
  if (!found) throw new Error("no composer");
  return found as HTMLTextAreaElement;
}

function optionButton(label: string): HTMLButtonElement {
  const found = [...(host?.querySelectorAll("button") ?? [])].find(
    (b) => b.textContent?.trim() === label,
  );
  if (!found) throw new Error(`no button labelled ${label}`);
  return found as HTMLButtonElement;
}

async function click(el: HTMLElement) {
  await act(async () => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = undefined;
  host = undefined;
  vi.unstubAllGlobals();
});

describe("GrillConversation while stalled", () => {
  it("offers Resume on a stall inside a round", async () => {
    await render(stalled, [round]);
    expect(resumeButton()).toBeDefined();
  });

  it("offers Resume on a stall with nothing asked yet, beside an open composer", async () => {
    await render(stalled, []);
    expect(resumeButton()).toBeDefined();
    expect(composer().disabled).toBe(false);
    expect(composer().placeholder).toBe("Type your answer…");
  });

  it("resumes on the bare endpoint, sending no answer with it", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ ...stalled, state: "running", parked_reason: "" }),
    } as Response);
    vi.stubGlobal("fetch", fetchMock);

    await render(stalled, [round]);
    await click(resumeButton() as HTMLButtonElement);

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/grill/1/resume");
    expect(init.method).toBe("POST");
    expect(init.body).toBeUndefined();
  });

  // Answering is the other way out of a stall, so the round the session died in stays
  // answerable beside the button.
  it("keeps the open round answerable", async () => {
    await render(stalled, [round]);
    expect(optionButton("login").disabled).toBe(false);
    const boxes = [
      ...(host?.querySelectorAll("textarea:not([readonly])") ?? []),
    ] as HTMLTextAreaElement[];
    expect(boxes.length).toBe(2);
    expect(boxes.every((t) => !t.disabled)).toBe(true);
  });

  it("keeps a single pending question answerable", async () => {
    await render(stalled, [
      {
        ...round,
        payload: { text: "Ship behind a flag?", options: ["yes", "no"] },
      },
    ]);
    expect(optionButton("yes").disabled).toBe(false);
    expect(composer().disabled).toBe(false);
  });

  it("holds the composer down while the round is open", async () => {
    await render(stalled, [round]);
    expect(composer().disabled).toBe(true);
    expect(composer().placeholder).toBe("Answer the round above…");
  });

  it("renders no Resume button once the session is running again", async () => {
    await render({ ...stalled, state: "running", parked_reason: "" }, [round]);
    expect(resumeButton()).toBeUndefined();
  });
});
