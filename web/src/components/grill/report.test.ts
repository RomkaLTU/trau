// @vitest-environment happy-dom
import {
  notifyManager,
  QueryClient,
  QueryClientProvider,
} from "@tanstack/react-query";
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { GrillSession, OutcomePayload } from "@/lib/grill";

import { ReportDocument } from "./report";

(
  globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true;

notifyManager.setScheduler((cb) => cb());

afterEach(() => vi.unstubAllGlobals());

const session: GrillSession = {
  id: "7",
  repo: "loop",
  issue_title: "Which retry policy does the SDK use?",
  state: "finished",
  mode: "research",
  provider: "claude",
  model: "opus-5",
  created_at: "2026-07-17T10:00:00Z",
  updated_at: "2026-07-19T14:30:00Z",
};

const outcome: OutcomePayload = {
  disposition: "research",
  title: "How the SDK retries",
  findings:
    "# Question\n\nWhich policy?\n\n## Conclusion\n\nExponential backoff.",
  summary: "The SDK backs off exponentially.",
};

async function render(
  container: HTMLElement,
  props: Omit<Parameters<typeof ReportDocument>[0], "repo">,
) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  document.body.appendChild(container);
  const root = createRoot(container);
  await act(async () => {
    root.render(
      createElement(
        QueryClientProvider,
        { client },
        createElement(ReportDocument, { repo: "loop", ...props }),
      ),
    );
  });
  return () => {
    root.unmount();
    container.remove();
  };
}

function buttonNamed(container: HTMLElement, label: string): HTMLButtonElement {
  const button = [...container.querySelectorAll("button")].find(
    (b) => b.textContent?.trim() === label,
  );
  if (!button) throw new Error(`no ${label} button`);
  return button;
}

function buttonLabelled(root: ParentNode, label: string): HTMLButtonElement {
  const button = root.querySelector<HTMLButtonElement>(
    `button[aria-label="${label}"]`,
  );
  if (!button) throw new Error(`no ${label} button`);
  return button;
}

// React tracks an input's value behind the DOM property, so a test types through the
// native setter for the change to reach the component.
function typeInto(input: HTMLInputElement, text: string) {
  const setValue = Object.getOwnPropertyDescriptor(
    HTMLInputElement.prototype,
    "value",
  )?.set as (this: HTMLInputElement, value: string) => void;
  setValue.call(input, text);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

function jsonResponse(status: number, body: unknown): Response {
  return {
    ok: status < 400,
    status,
    json: () => Promise.resolve(body),
  } as Response;
}

describe("ReportDocument", () => {
  it("leads with the report's own title and dates what wrote it", async () => {
    const container = document.createElement("div");
    const unmount = await render(container, {
      session,
      outcome,
      warnings: [],
    });
    expect(container.querySelector("h1")?.textContent).toBe(
      "How the SDK retries",
    );
    const meta = container.querySelector("p")?.textContent ?? "";
    expect(meta).toContain("2026");
    expect(meta).toContain("claude · opus-5");
    unmount();
  });

  it("renders the findings as a document, unclamped and with real headings", async () => {
    const container = document.createElement("div");
    const unmount = await render(container, {
      session,
      outcome,
      warnings: [],
    });
    expect(container.querySelector("h2")?.textContent).toBe("Question");
    expect(container.querySelector("h3")?.textContent).toBe("Conclusion");
    expect(container.querySelector("[class*=max-h-]")).toBeNull();
    expect(container.textContent).toContain("The SDK backs off exponentially.");
    unmount();
  });

  it("names a titleless report by the question it opened on", async () => {
    const container = document.createElement("div");
    const unmount = await render(container, {
      session,
      outcome: { ...outcome, title: "" },
      warnings: [],
    });
    expect(container.querySelector("h1")?.textContent).toBe(
      "Which retry policy does the SDK use?",
    );
    unmount();
  });

  it("lists the sources it cited, linked and named by their domain", async () => {
    const container = document.createElement("div");
    const unmount = await render(container, {
      session,
      outcome: {
        ...outcome,
        sources: [
          {
            title: "Retry docs",
            url: "https://www.sdk.example/retries",
            note: "the backoff table",
          },
          { title: "Release notes", url: "https://blog.other.example/v3" },
        ],
      },
      warnings: [],
    });
    const items = [...container.querySelectorAll("ol > li")];
    expect(items).toHaveLength(2);
    const first = items[0].querySelector("a") as HTMLAnchorElement;
    expect(first.textContent).toBe("Retry docs");
    expect(first.href).toBe("https://www.sdk.example/retries");
    expect(first.target).toBe("_blank");
    expect(items[0].textContent).toContain("sdk.example · the backoff table");
    expect(items[1].textContent).toContain("blog.other.example");
    unmount();
  });

  it("renders no Sources section when the research cited none", async () => {
    const container = document.createElement("div");
    const unmount = await render(container, {
      session,
      outcome: { ...outcome, sources: [] },
      warnings: [],
    });
    expect(container.textContent).not.toContain("Sources");
    expect(container.querySelector("ol")).toBeNull();
    unmount();
  });

  it("copies the composed Markdown, and confirms it", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });
    const container = document.createElement("div");
    const unmount = await render(container, {
      session,
      outcome,
      warnings: [],
    });
    const copy = buttonNamed(container, "Copy");
    await act(async () => copy.click());
    expect(writeText).toHaveBeenCalledTimes(1);
    const written = writeText.mock.calls[0][0] as string;
    expect(written).toContain("# How the SDK retries");
    expect(written).toContain("Exponential backoff.");
    expect(buttonNamed(container, "Copied")).toBeDefined();
    unmount();
  });

  it("downloads the report as a dated .md file", async () => {
    const created = vi.fn().mockReturnValue("blob:report");
    URL.createObjectURL = created;
    URL.revokeObjectURL = vi.fn();
    const clicked: HTMLAnchorElement[] = [];
    const click = vi
      .spyOn(HTMLAnchorElement.prototype, "click")
      .mockImplementation(function (this: HTMLAnchorElement) {
        clicked.push(this);
      });
    const container = document.createElement("div");
    const unmount = await render(container, {
      session,
      outcome,
      warnings: [],
    });
    await act(async () => buttonNamed(container, "Download .md").click());
    expect(clicked).toHaveLength(1);
    expect(clicked[0].download).toBe("2026-07-19-how-the-sdk-retries.md");
    expect(created.mock.calls[0][0]).toBeInstanceOf(Blob);
    expect(document.querySelector("a[download]")).toBeNull();
    click.mockRestore();
    unmount();
  });

  it("exports a PDF through the browser's print dialog", async () => {
    const print = vi.fn();
    window.print = print;
    const container = document.createElement("div");
    const unmount = await render(container, {
      session,
      outcome,
      warnings: [],
    });
    await act(async () => buttonNamed(container, "Export PDF").click());
    expect(print).toHaveBeenCalledTimes(1);
    unmount();
  });

  it("renames the report inline and reads it under the name the hub kept", async () => {
    const renamed: GrillSession = {
      ...session,
      report_title: "SDK retry behaviour",
    };
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, renamed));
    vi.stubGlobal("fetch", fetchMock);
    const seen: GrillSession[] = [];
    const container = document.createElement("div");
    const unmount = await render(container, {
      session,
      outcome,
      warnings: [],
      onSession: (next) => seen.push(next),
    });

    await act(async () => buttonLabelled(container, "Rename report").click());
    const input = container.querySelector("input") as HTMLInputElement;
    expect(input.value).toBe("How the SDK retries");
    await act(async () => {
      typeInto(input, "SDK retry behaviour");
      input.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Enter", bubbles: true }),
      );
    });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/grill/7/title");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({
      title: "SDK retry behaviour",
    });
    expect(seen).toEqual([renamed]);
    unmount();
  });

  it("leaves the hub alone when the rename changes nothing", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    const container = document.createElement("div");
    const unmount = await render(container, {
      session,
      outcome,
      warnings: [],
    });

    await act(async () => buttonLabelled(container, "Rename report").click());
    const input = container.querySelector("input") as HTMLInputElement;
    await act(async () => {
      typeInto(input, "   ");
      input.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Enter", bubbles: true }),
      );
    });

    expect(fetchMock).not.toHaveBeenCalled();
    expect(container.querySelector("h1")?.textContent).toContain(
      "How the SDK retries",
    );
    unmount();
  });

  it("offers no delete until the report is applied", async () => {
    const container = document.createElement("div");
    const unmount = await render(container, {
      session,
      outcome,
      warnings: [],
    });
    expect(
      [...container.querySelectorAll("button")].map((b) => b.textContent),
    ).not.toContain("Delete");
    unmount();
  });

  it("deletes an applied report once the warning is confirmed", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue({ ok: true, status: 204 } as Response);
    vi.stubGlobal("fetch", fetchMock);
    let deleted = 0;
    const container = document.createElement("div");
    const unmount = await render(container, {
      session: { ...session, state: "applied" },
      outcome,
      warnings: [],
      onDeleted: () => deleted++,
    });

    await act(async () => buttonNamed(container, "Delete").click());
    expect(document.body.textContent).toContain("cannot be recovered");
    await act(async () => buttonNamed(document.body, "Delete forever").click());

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/grill/7");
    expect(init.method).toBe("DELETE");
    expect(deleted).toBe(1);
    unmount();
  });

  it("says so when the session recorded no report, and raises apply warnings", async () => {
    const container = document.createElement("div");
    const unmount = await render(container, {
      session: { ...session, state: "applied" },
      outcome: { ...outcome, findings: "   " },
      warnings: ["The comment could not be posted."],
    });
    expect(container.textContent).toContain("This session recorded no report.");
    expect(container.textContent).toContain("The comment could not be posted.");
    unmount();
  });
});
