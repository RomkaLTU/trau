// @vitest-environment happy-dom
import {
  notifyManager,
  QueryClient,
  QueryClientProvider,
} from "@tanstack/react-query";
import { act, createElement, Fragment } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Toaster } from "sonner";
import { afterEach, describe, expect, it, vi } from "vitest";

import { CreatedBannerProvider } from "@/components/trau/created-banner";
import type { AzureCreateOptions } from "@/lib/azure";
import type {
  GrillApplyResponse,
  GrillListResponse,
  GrillSession,
  OutcomePayload,
} from "@/lib/grill";
import type { Issue } from "@/lib/issues";

import { OutcomeReview } from "./outcome-review";

(
  globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true;

notifyManager.setScheduler((cb) => cb());

const session: GrillSession = {
  id: "1",
  repo: "loop",
  issue_id: "COD-42",
  state: "finished",
  created_at: "2026-07-17T10:00:00Z",
  updated_at: "2026-07-17T10:00:00Z",
};

const rewrite: OutcomePayload = {
  disposition: "rewrite",
  proposed_description: "A crisp new description.",
  summary: "clarified the flow",
};

const createEpic: OutcomePayload = {
  disposition: "create",
  title: "New epic",
  proposed_description: "Epic body.",
  summary: "one slice",
  sub_issues: [{ title: "S1", description: "d1" }],
};

function issue(source: string): Issue {
  return {
    repo: "loop",
    provider: "linear",
    id: "COD-42",
    title: "Checkout rewrite",
    description: "The old body.",
    status: "Todo",
    group: "unstarted",
    labels: [],
    ready: false,
    source,
    has_children: false,
    children: 0,
    comments: [],
    in_project: true,
  };
}

let root: Root | undefined;
let host: HTMLElement | undefined;

const azureOptions: AzureCreateOptions = {
  types: ["User Story", "Bug"],
  features: [{ id: "70", title: "Checkout" }],
};

// renderReview mounts the review on seeded caches, so the only fetch a test sees
// is the apply it drives itself. The create-options entry is seeded warm the way
// any create session in the panel leaves it — it is keyed by repo, not by session.
// The toaster rides along because a landed apply raises its caveats there rather
// than in the review the host retires.
function renderReview(
  outcome: OutcomePayload,
  tracker: string,
  anchorSource: string,
  open: GrillSession = session,
) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  client.setQueryData<GrillListResponse>(["grill", "loop"], {
    repo: "loop",
    tracker,
    sessions: [open],
  });
  client.setQueryData<Issue>(["issue", "loop", "COD-42"], issue(anchorSource));
  client.setQueryData(["assignable-users", "loop", ""], []);
  client.setQueryData<AzureCreateOptions>(
    ["azure-create-options", "loop"],
    azureOptions,
  );
  host = document.createElement("div");
  document.body.appendChild(host);
  const mounted = createRoot(host);
  root = mounted;
  act(() => {
    mounted.render(
      createElement(
        QueryClientProvider,
        { client },
        createElement(
          CreatedBannerProvider,
          null,
          createElement(
            Fragment,
            null,
            createElement(OutcomeReview, {
              repo: "loop",
              issueId: "COD-42",
              session: open,
              outcome,
              onSession: () => {},
            }),
            createElement(Toaster),
          ),
        ),
      ),
    );
  });
  return host;
}

function buttons(): HTMLButtonElement[] {
  return Array.from(host?.querySelectorAll("button") ?? []);
}

// The destination options are buttons too, and one of them reads "Apply to
// COD-42 on Linear", so the action buttons are matched on their whole label.
function button(label: string): HTMLButtonElement {
  const found = buttons().find((b) => b.textContent?.trim() === label);
  if (!found) throw new Error(`no button labelled ${label}`);
  return found;
}

function jsonResponse(status: number, body: unknown): Response {
  return {
    ok: status < 400,
    status,
    json: () => Promise.resolve(body),
  } as Response;
}

function applyResponse(over: Partial<GrillApplyResponse>): GrillApplyResponse {
  return { session, applied: true, steps: [], ...over };
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = undefined;
  host = undefined;
  vi.unstubAllGlobals();
});

describe("OutcomeReview destination", () => {
  it("offers the conversion on a rewrite anchored to a tracker ticket", () => {
    const el = renderReview(rewrite, "linear", "linear");

    expect(el.textContent).toContain("Apply to COD-42 on Linear");
    expect(el.textContent).toContain(
      "Convert COD-42 (and its sub-issues) to internal and apply there",
    );
  });

  it("offers the conversion on a create anchored to the parent it filed on the tracker", () => {
    const el = renderReview(createEpic, "linear", "linear", {
      ...session,
      issue_destination: "tracker",
    });

    expect(el.textContent).toContain(
      "Convert COD-42 (and its sub-issues) to internal and apply there",
    );
  });

  it("states the one destination when the anchor is already internal", () => {
    const el = renderReview(rewrite, "linear", "internal");

    expect(el.textContent).toContain("in this repo's internal backlog");
    expect(el.textContent).not.toContain("Convert COD-42");
  });

  it("offers no choice on a repo with no external tracker", () => {
    const el = renderReview(rewrite, "internal", "internal");

    expect(el.textContent).not.toContain("Apply to COD-42 on");
    expect(el.textContent).not.toContain("Convert COD-42");
  });

  it("withholds the choice from a needs_split outcome", () => {
    const el = renderReview(
      { disposition: "needs_split", summary: "too big" },
      "linear",
      "linear",
    );

    expect(el.textContent).not.toContain("Destination");
  });
});

describe("OutcomeReview Azure hierarchy", () => {
  it("offers the work-item type and the Feature on a create", () => {
    const el = renderReview(createEpic, "azure", "azure");

    expect(el.textContent).toContain("Work item type");
    expect(el.textContent).toContain("No Feature");
  });

  it("withholds both from a rewrite, whose choice the hub would discard", () => {
    const el = renderReview(rewrite, "azure", "azure");

    expect(el.textContent).not.toContain("Work item type");
    expect(el.textContent).not.toContain("No Feature");
  });
});

describe("OutcomeReview recovery", () => {
  it("offers the conversion after a failed tracker apply and re-applies internally", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse(
        200,
        applyResponse({
          applied: false,
          steps: [
            { step: "description", status: "failed", error: "linear: 500" },
          ],
        }),
      ),
    );
    vi.stubGlobal("fetch", fetchMock);
    renderReview(rewrite, "linear", "linear");

    await act(async () => button("Apply").click());
    expect(button("Convert and apply internally")).toBeDefined();

    await act(async () => button("Convert and apply internally").click());

    const calls = fetchMock.mock.calls;
    const last = calls[calls.length - 1][1] as RequestInit;
    expect(JSON.parse(String(last.body))).toMatchObject({
      destination: "internal",
    });
  });

  it("surfaces the warnings a landed conversion carries", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse(
        200,
        applyResponse({
          applied: false,
          steps: [{ step: "comment", status: "failed", error: "boom" }],
          warnings: ["COD-42: the superseded note failed: linear: 503"],
        }),
      ),
    );
    vi.stubGlobal("fetch", fetchMock);
    const el = renderReview(rewrite, "linear", "linear");

    await act(async () => button("Apply").click());

    expect(el.textContent).toContain("the superseded note failed: linear: 503");
  });
});

describe("OutcomeReview caveats", () => {
  it("raises a landed apply's warnings where the host retiring the review cannot take them", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse(
        200,
        applyResponse({
          warnings: ["COD-42: the superseded note failed: linear: 503"],
        }),
      ),
    );
    vi.stubGlobal("fetch", fetchMock);
    renderReview(rewrite, "linear", "linear");

    await act(async () => button("Apply").click());

    const toast = document.querySelector("[data-sonner-toaster]");
    expect(toast?.textContent).toContain("Applied, with caveats.");
    expect(toast?.textContent).toContain(
      "the superseded note failed: linear: 503",
    );
  });
});

describe("OutcomeReview applied card", () => {
  it("reads the destination and the warnings back off the settled session", () => {
    const el = renderReview(rewrite, "linear", "internal", {
      ...session,
      issue_id: "ACME-1",
      issue_destination: "internal",
      apply_warnings: ["COD-42: the superseded note failed: linear: 503"],
      state: "applied",
    });

    expect(el.textContent).toContain("ACME-1 in the internal backlog");
    expect(el.textContent).toContain("the superseded note failed: linear: 503");
  });
});
