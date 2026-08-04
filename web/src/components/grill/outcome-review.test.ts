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
import type { QueueItem, QueueResponse } from "@/lib/queue";

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

const createTicket: OutcomePayload = {
  disposition: "create",
  title: "New ticket",
  proposed_description: "Ticket body.",
  summary: "one ticket",
};

const research: OutcomePayload = {
  disposition: "research",
  title: "How the SDK retries",
  findings: "## Conclusion\n\nExponential backoff.",
  summary: "The SDK backs off exponentially.",
};

const filedEpic: GrillSession = {
  ...session,
  issue_id: "COD-77",
  issue_title: "New epic",
};

const filedTicket: GrillSession = {
  ...session,
  issue_id: "COD-78",
  issue_title: "New ticket",
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
let client: QueryClient | undefined;

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
  reportShown = false,
) {
  const seeded = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  client = seeded;
  seeded.setQueryData<GrillListResponse>(["grill", "loop"], {
    repo: "loop",
    tracker,
    sessions: [open],
  });
  seeded.setQueryData<Issue>(["issue", "loop", "COD-42"], issue(anchorSource));
  seeded.setQueryData(["assignable-users", "loop", ""], []);
  seeded.setQueryData<AzureCreateOptions>(
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
        { client: seeded },
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
              reportShown,
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
  client = undefined;
  vi.unstubAllGlobals();
});

describe("OutcomeReview research", () => {
  it("carries the report itself where nothing else shows it", () => {
    const el = renderReview(research, "linear", "linear");

    expect(el.textContent).toContain("Research report");
    expect(el.textContent).toContain("Exponential backoff.");
    expect(el.textContent).toContain("The SDK backs off exponentially.");
  });

  it("is the decision alone once the document carries the report", () => {
    const el = renderReview(research, "linear", "linear", session, true);

    expect(el.textContent).not.toContain("Exponential backoff.");
    expect(el.textContent).not.toContain("The SDK backs off exponentially.");
    expect(el.textContent).toContain(
      "Posts the report as a comment on the issue.",
    );
    expect(button("Approve")).toBeTruthy();
  });
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

// serveCreate routes the two calls a queued create makes: the apply, taken in
// order so a retry can answer differently from the first attempt, and the queue
// add. Every other queue call reads the rows given, which is what the re-add
// paths inspect when the add is refused.
function serveCreate(
  applies: GrillApplyResponse[],
  add: Response = jsonResponse(200, queue([])),
  rows: QueueItem[] = [],
) {
  const pending = [...applies];
  const fetchMock = vi.fn((input: string, init?: RequestInit) => {
    if (input.includes("/apply")) {
      const next = pending.length > 1 ? pending.shift()! : pending[0];
      return Promise.resolve(jsonResponse(200, next));
    }
    if (init?.method === "POST") return Promise.resolve(add);
    return Promise.resolve(jsonResponse(200, queue(rows)));
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function queue(items: QueueItem[]): QueueResponse {
  return { repo: "loop", draining: false, stopping: false, items };
}

function queueAdds(fetchMock: { mock: { calls: unknown[][] } }): unknown[] {
  return fetchMock.mock.calls
    .filter(([input, init]) => {
      const req = init as RequestInit | undefined;
      return String(input).endsWith("/queue") && req?.method === "POST";
    })
    .map(([, init]) => JSON.parse(String((init as RequestInit).body)));
}

// The chevron opens on pointerdown, which is what the menu's trigger listens
// for, and the menu itself lands in a portal outside the review's host.
async function pickCreateAndQueue() {
  const chevron = host?.querySelector<HTMLElement>(
    '[aria-label="More create actions"]',
  );
  if (!chevron) throw new Error("no create menu trigger");
  await act(async () => {
    chevron.dispatchEvent(
      new PointerEvent("pointerdown", { bubbles: true, button: 0 }),
    );
  });
  const item = Array.from(
    document.querySelectorAll<HTMLElement>('[role="menuitem"]'),
  ).find((el) => el.textContent?.trim() === "Create and queue");
  if (!item) throw new Error("no Create and queue item");
  await act(async () => item.click());
}

describe("OutcomeReview create and queue", () => {
  it("offers the subaction on a create and nowhere else", () => {
    const el = renderReview(createEpic, "linear", "linear");
    expect(
      el.querySelector('[aria-label="More create actions"]'),
    ).not.toBeNull();

    act(() => root?.unmount());
    host?.remove();
    const rewritten = renderReview(rewrite, "linear", "linear");
    expect(
      rewritten.querySelector('[aria-label="More create actions"]'),
    ).toBeNull();
  });

  it("leaves the queue alone on a plain Create", async () => {
    const fetchMock = serveCreate([applyResponse({ session: filedTicket })]);
    renderReview(createTicket, "linear", "linear");

    await act(async () => button("Create").click());

    expect(queueAdds(fetchMock)).toEqual([]);
  });

  it("adds a single create to the back as a ticket", async () => {
    const fetchMock = serveCreate([applyResponse({ session: filedTicket })]);
    renderReview(createTicket, "linear", "linear");

    await pickCreateAndQueue();

    expect(queueAdds(fetchMock)).toEqual([
      { id: "COD-78", kind: "ticket", title: "New ticket" },
    ]);
  });

  it("adds an epic create as the parent so its slices run in order", async () => {
    const row: QueueItem = {
      position: 1,
      kind: "epic",
      id: "COD-77",
      status: "pending",
    };
    const fetchMock = serveCreate(
      [applyResponse({ session: filedEpic })],
      jsonResponse(200, queue([row])),
    );
    renderReview(createEpic, "linear", "linear");

    await pickCreateAndQueue();

    expect(queueAdds(fetchMock)).toEqual([
      { id: "COD-77", kind: "epic", title: "New epic" },
    ]);
    expect(
      client?.getQueryData<QueueResponse>(["queue", "loop"])?.items,
    ).toEqual([row]);
  });

  it("carries the intent into the internal fallback", async () => {
    const fetchMock = serveCreate([
      applyResponse({
        session: { ...session, issue_id: "" },
        applied: false,
        steps: [
          { step: "description", status: "failed", error: "linear: 500" },
        ],
      }),
      applyResponse({ session: filedEpic }),
    ]);
    renderReview(createEpic, "linear", "linear");

    await pickCreateAndQueue();
    expect(queueAdds(fetchMock)).toEqual([]);

    await act(async () => button("File internally instead").click());

    expect(queueAdds(fetchMock)).toEqual([
      { id: "COD-77", kind: "epic", title: "New epic" },
    ]);
  });

  it("reads a row the queue already holds as queued", async () => {
    const row: QueueItem = {
      position: 1,
      kind: "epic",
      id: "COD-77",
      status: "pending",
    };
    const fetchMock = serveCreate(
      [applyResponse({ session: filedEpic })],
      jsonResponse(409, { error: "COD-77 is already queued" }),
      [row],
    );
    renderReview(createEpic, "linear", "linear");

    await pickCreateAndQueue();

    expect(queueAdds(fetchMock)).toHaveLength(1);
    const toast = document.querySelector("[data-sonner-toaster]");
    expect(toast?.textContent ?? "").not.toContain("adding it to the queue");
    expect(
      client?.getQueryData<QueueResponse>(["queue", "loop"])?.items,
    ).toEqual([row]);
  });

  it("raises a caveat when the create lands but the queue add fails", async () => {
    serveCreate(
      [applyResponse({ session: filedEpic })],
      jsonResponse(500, { error: "queue is sealed" }),
    );
    renderReview(createEpic, "linear", "linear");

    await pickCreateAndQueue();

    const toast = document.querySelector("[data-sonner-toaster]");
    expect(toast?.textContent).toContain(
      "COD-77 was created but adding it to the queue failed: queue is sealed",
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
