// @vitest-environment happy-dom
import {
  notifyManager,
  QueryClient,
  QueryClientProvider,
} from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterContextProvider,
} from "@tanstack/react-router";
import { act, createElement, Fragment, type ComponentProps } from "react";
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

import { applyLabel, applyStatusLine, OutcomeReview } from "./outcome-review";

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

// A Link builds its href off the router in context, so the card's receipt needs
// one mounted even though nothing here navigates.
const rootRoute = createRootRoute();
const testRouter = createRouter({
  routeTree: rootRoute.addChildren([
    createRoute({ getParentRoute: () => rootRoute, path: "/loop" }),
    createRoute({ getParentRoute: () => rootRoute, path: "/backlog" }),
  ]),
  history: createMemoryHistory({ initialEntries: ["/"] }),
}) as unknown as ComponentProps<typeof RouterContextProvider>["router"];

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
  const tree = createElement(
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
  );
  act(() => {
    mounted.render(
      createElement(RouterContextProvider, {
        router: testRouter,
        children: tree,
      }),
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

describe("applyLabel", () => {
  it("names the gesture the button takes", () => {
    expect(applyLabel("rewrite", false)).toBe("Apply");
    expect(applyLabel("create", false)).toBe("Create");
    expect(applyLabel("research", false)).toBe("Approve");
    expect(applyLabel("no_change", false)).toBe("Close out");
  });

  it("names the gesture in flight while the apply is pending", () => {
    expect(applyLabel("rewrite", true)).toBe("Applying…");
    expect(applyLabel("split", true)).toBe("Applying…");
    expect(applyLabel("needs_split", true)).toBe("Applying…");
    expect(applyLabel("create", true)).toBe("Creating…");
    expect(applyLabel("research", true)).toBe("Approving…");
    expect(applyLabel("no_change", true)).toBe("Closing out…");
  });

  it("offers the retry only once the partial apply has settled", () => {
    const partial = applyResponse({ applied: false });

    expect(applyLabel("rewrite", false, partial)).toBe("Retry");
    expect(applyLabel("rewrite", true, partial)).toBe("Applying…");
  });
});

describe("applyStatusLine", () => {
  const filing = {
    disposition: "create",
    tracker: "linear",
    destination: "tracker" as const,
    anchor: "COD-42",
    subCount: 0,
    slow: false,
  };

  it("counts an epic's slices into what is being filed", () => {
    expect(applyStatusLine({ ...filing, subCount: 3 })).toBe(
      "Filing epic + 3 sub-issues to Linear…",
    );
    expect(applyStatusLine({ ...filing, subCount: 1 })).toBe(
      "Filing epic + 1 sub-issue to Linear…",
    );
    expect(applyStatusLine(filing)).toBe("Filing to Linear…");
  });

  it("names the backlog a create files to instead of the tracker", () => {
    expect(applyStatusLine({ ...filing, destination: "internal" })).toBe(
      "Filing to the internal backlog…",
    );
    expect(
      applyStatusLine({ ...filing, tracker: "internal", subCount: 2 }),
    ).toBe("Filing epic + 2 sub-issues to the internal backlog…");
  });

  it("names the ticket an anchored outcome writes to", () => {
    const rewriting = { ...filing, disposition: "rewrite" };

    expect(applyStatusLine({ ...rewriting, tracker: "jira" })).toBe(
      "Applying to COD-42 on Jira…",
    );
    expect(applyStatusLine({ ...rewriting, disposition: "research" })).toBe(
      "Applying to COD-42 on Linear…",
    );
    expect(applyStatusLine({ ...rewriting, destination: "internal" })).toBe(
      "Applying to COD-42 in the internal backlog…",
    );
  });

  it("leaves the destination out of what writes nowhere", () => {
    expect(applyStatusLine({ ...filing, disposition: "no_change" })).toBe(
      "Closing out…",
    );
    expect(
      applyStatusLine({ ...filing, disposition: "research", anchor: "" }),
    ).toBe("Applying…");
  });

  it("blames the tracker by name once the apply is taking its time", () => {
    expect(applyStatusLine({ ...filing, subCount: 3, slow: true })).toBe(
      "Filing epic + 3 sub-issues to Linear… Still working — Linear can be slow.",
    );
    expect(
      applyStatusLine({ ...filing, destination: "internal", slow: true }),
    ).toBe(
      "Filing to the internal backlog… Still working — this can take a moment.",
    );
  });
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

describe("OutcomeReview requeue", () => {
  const appliedFix: GrillSession = {
    ...session,
    mode: "fix",
    state: "applied",
  };

  // A transient failure the diagnosis found nothing to rewrite for is exactly the
  // one worth retrying, so the disposition never gates the offer.
  it("offers the requeue on an applied fix session whatever it decided", () => {
    renderReview(
      { disposition: "no_change", summary: "transient CI flake" },
      "linear",
      "linear",
      appliedFix,
    );

    expect(button("Requeue COD-42")).toBeDefined();
  });

  it("withholds it from a session that was not diagnosing a failure", () => {
    const el = renderReview(rewrite, "linear", "linear", {
      ...appliedFix,
      mode: "interview",
    });

    expect(el.textContent).not.toContain("Requeue COD-42");
  });

  it("settles into a receipt of what the requeue changed", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse(200, {
        ticket: "COD-42",
        changed: ["closed the attempt PR 42", "cleared the saved checkpoint"],
      }),
    );
    vi.stubGlobal("fetch", fetchMock);
    const el = renderReview(rewrite, "linear", "linear", appliedFix);

    await act(async () => button("Requeue COD-42").click());

    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/repos/loop/runs/COD-42/requeue",
      expect.objectContaining({ method: "POST" }),
    );
    expect(el.textContent).toContain("closed the attempt PR 42");
    expect(el.textContent).toContain("COD-42 is eligible again");
    expect(buttons().some((b) => b.textContent?.includes("Requeue"))).toBe(
      false,
    );
  });

  it("keeps the button for a retry when the hub refuses", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse(409, {
        error: "COD-42 is already shipped (PR 42 is merged)",
      }),
    );
    vi.stubGlobal("fetch", fetchMock);
    const el = renderReview(rewrite, "linear", "linear", appliedFix);

    await act(async () => button("Requeue COD-42").click());

    expect(el.textContent).toContain("COD-42 is already shipped");
    expect(button("Requeue COD-42")).toBeDefined();
  });
});

describe("OutcomeReview applying", () => {
  it("says what it is filing and freezes the payload while the apply is in flight", async () => {
    let land = (_: Response) => {};
    const inFlight = new Promise<Response>((resolve) => {
      land = resolve;
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(() => inFlight),
    );
    const el = renderReview(createEpic, "linear", "linear");

    await act(async () => button("Create").click());

    expect(el.textContent).toContain("Filing epic + 1 sub-issue to Linear…");
    expect(button("Creating…").disabled).toBe(true);
    const fields = Array.from(
      el.querySelectorAll<HTMLInputElement | HTMLTextAreaElement>(
        "input, textarea",
      ),
    );
    expect(fields.length).toBeGreaterThan(0);
    expect(fields.every((field) => field.disabled)).toBe(true);
    expect(buttons().every((b) => b.disabled)).toBe(true);

    await act(async () => {
      land(jsonResponse(200, applyResponse({ session: filedEpic })));
    });

    expect(el.textContent).not.toContain("Filing epic");
  });
});
