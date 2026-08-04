// @vitest-environment happy-dom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { describe, expect, it } from "vitest";

import type { GrillSession, OutcomePayload } from "@/lib/grill";

import { ReportDocument } from "./report";

(
  globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true;

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
  props: Parameters<typeof ReportDocument>[0],
) {
  const root = createRoot(container);
  await act(async () => {
    root.render(createElement(ReportDocument, props));
  });
  return () => root.unmount();
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
