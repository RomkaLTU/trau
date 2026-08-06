// @vitest-environment happy-dom
import { act, createElement, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it } from "vitest";

import { RunOptions } from "./run-steps-dialog";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

let root: Root | undefined;

function renderOptions(initial: string[] = [], initialOpen = false) {
  const seen: string[][] = [];
  function Probe() {
    const [skips, setSkips] = useState(initial);
    const [open, setOpen] = useState(initialOpen);
    return createElement(RunOptions, {
      skips,
      onChange: (next: string[]) => {
        seen.push(next);
        setSkips(next);
      },
      open,
      onOpenChange: setOpen,
    });
  }
  const container = document.createElement("div");
  document.body.appendChild(container);
  const mounted = createRoot(container);
  root = mounted;
  act(() => mounted.render(createElement(Probe)));
  return { seen };
}

function toggle(label: string) {
  const box = [...document.body.querySelectorAll('[role="checkbox"]')].find(
    (b) => b.textContent?.startsWith(label),
  );
  if (!box) throw new Error(`no toggle labelled ${label}`);
  act(() => (box as HTMLElement).click());
}

function toggleLabels(): string[] {
  return [...document.body.querySelectorAll('[role="checkbox"]')].map(
    (b) => b.querySelector("span.font-mono")?.textContent?.trim() ?? "",
  );
}

afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  document.body.innerHTML = "";
});

describe("RunOptions", () => {
  it("stays collapsed and offers the five skippable Activities", () => {
    renderOptions();

    const details = document.body.querySelector("details");
    expect(details?.hasAttribute("open")).toBe(false);
    expect(toggleLabels()).toEqual([
      "Lint-fix",
      "Cleanup",
      "Verify",
      "CI wait",
      "Auto-merge",
    ]);
  });

  it("skips nothing until an Activity is unticked", () => {
    const { seen } = renderOptions();

    toggle("Verify");

    expect(seen).toEqual([["verify"]]);
  });

  it("re-ticking an Activity drops it from the set", () => {
    const { seen } = renderOptions(["verify", "merge"]);

    toggle("Verify");

    expect(seen).toEqual([["merge"]]);
  });

  it("summarises the chosen set on the collapsed summary", () => {
    renderOptions(["verify", "ci"]);

    expect(document.body.querySelector("summary")?.textContent).toContain(
      "verify, ci skipped",
    );
  });

  it("opens on the set a form seeds it with", () => {
    renderOptions(["verify", "ci"], true);

    const details = document.body.querySelector("details");
    expect(details?.hasAttribute("open")).toBe(true);
    const unticked = [...document.body.querySelectorAll('[role="checkbox"]')]
      .filter((b) => b.getAttribute("aria-checked") === "false")
      .map((b) => b.querySelector("span.font-mono")?.textContent?.trim());
    expect(unticked).toEqual(["Verify", "CI wait"]);
  });

  it("warns while nothing checks the slice", () => {
    renderOptions(["verify"]);

    expect(document.body.textContent).toContain(
      "Nothing checks this slice before it ships",
    );
  });
});
