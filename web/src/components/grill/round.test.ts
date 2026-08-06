// @vitest-environment happy-dom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { describe, expect, it, vi } from "vitest";

import { RoundForm } from "./round";
import { uploadAttachments } from "@/lib/attachments";
import type { RoundAnswer, RoundQuestion } from "@/lib/grill";

vi.mock("@/lib/attachments", () => ({
  uploadAttachments: vi.fn(),
}));
vi.mock("sonner", () => ({
  toast: {
    loading: vi.fn(() => "toast"),
    dismiss: vi.fn(),
    error: vi.fn(),
  },
}));

(
  globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true;

const QUESTIONS: RoundQuestion[] = [
  {
    text: "Which page is in scope?",
    options: ["login", "signup"],
    recommended: "login",
    why: "It is the only page the ticket names.",
    allow_free_text: true,
  },
  { text: "Which auth flow?", options: [], allow_free_text: true },
  {
    text: "Ship behind a flag?",
    options: ["yes", "no"],
    allow_free_text: false,
  },
];

async function render(
  answers: RoundAnswer[] = [],
  onSubmit: (a: RoundAnswer[]) => void = () => {},
) {
  const container = document.createElement("div");
  document.body.append(container);
  const root = createRoot(container);
  await act(async () => {
    root.render(
      createElement(RoundForm, {
        repo: "loop",
        questions: QUESTIONS,
        answers,
        disabled: false,
        submitting: false,
        onSubmit,
      }),
    );
  });
  return {
    container,
    unmount: () => {
      root.unmount();
      container.remove();
    },
  };
}

function buttonNamed(container: HTMLElement, label: string): HTMLButtonElement {
  const found = [...container.querySelectorAll("button")].find((b) =>
    b.textContent?.includes(label),
  );
  if (!found) throw new Error(`no button labelled ${label}`);
  return found as HTMLButtonElement;
}

async function click(el: HTMLElement) {
  await act(async () => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

describe("RoundForm", () => {
  it("lists every question of the round, numbered, with the recommendation picked", async () => {
    const { container, unmount } = await render();
    expect(container.textContent).toContain("1.");
    expect(container.textContent).toContain("Which page is in scope?");
    expect(container.textContent).toContain("Which auth flow?");
    expect(container.textContent).toContain("3.");
    expect(container.textContent).toContain("Ship behind a flag?");
    expect(buttonNamed(container, "login").getAttribute("aria-pressed")).toBe(
      "true",
    );
    expect(buttonNamed(container, "signup").getAttribute("aria-pressed")).toBe(
      "false",
    );
    // Only the questions that take one get a free-text box: the third does not.
    expect(container.querySelectorAll("textarea")).toHaveLength(2);
    unmount();
  });

  it("submits the answers given, in any order, and holds back the ones left blank", async () => {
    const onSubmit = vi.fn();
    const { container, unmount } = await render([], onSubmit);

    await click(buttonNamed(container, "no"));
    const free = container.querySelectorAll("textarea")[1] as HTMLTextAreaElement;
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(
        HTMLTextAreaElement.prototype,
        "value",
      )?.set;
      setter?.call(free, "magic link");
      free.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click(buttonNamed(container, "Submit answers"));

    expect(onSubmit).toHaveBeenCalledWith([
      { index: 0, text: "login" },
      { index: 1, text: "magic link" },
      { index: 2, text: "no" },
    ]);
    unmount();
  });

  it("shows the questions auto-accept already settled as answered, and asks only for the rest", async () => {
    const onSubmit = vi.fn();
    const { container, unmount } = await render(
      [
        { index: 0, text: "login", auto: true },
        { index: 2, text: "yes", auto: true },
      ],
      onSubmit,
    );

    expect(container.textContent).toContain("2 of 3 answered");
    expect(container.querySelectorAll("textarea")).toHaveLength(1);
    expect(container.textContent).toContain("auto-accepted");

    const free = container.querySelector("textarea") as HTMLTextAreaElement;
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(
        HTMLTextAreaElement.prototype,
        "value",
      )?.set;
      setter?.call(free, "magic link");
      free.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click(buttonNamed(container, "Submit answers"));

    expect(onSubmit).toHaveBeenCalledWith([{ index: 1, text: "magic link" }]);
    unmount();
  });

  it("uploads an image pasted into an answer and submits its reference with the round", async () => {
    vi.mocked(uploadAttachments).mockResolvedValue({
      uploaded: [
        {
          id: 7,
          url: "/api/v1/repos/loop/attachments/7",
          filename: "shot.png",
          mime_type: "image/png",
        },
      ],
      errors: [],
    });
    const onSubmit = vi.fn();
    const { container, unmount } = await render([], onSubmit);

    const free = container.querySelectorAll("textarea")[1] as HTMLTextAreaElement;
    const clipboard = new DataTransfer();
    clipboard.items.add(new File(["png"], "shot.png", { type: "image/png" }));
    await act(async () => {
      free.dispatchEvent(
        new ClipboardEvent("paste", { bubbles: true, clipboardData: clipboard }),
      );
    });
    await click(buttonNamed(container, "Submit answers"));

    expect(uploadAttachments).toHaveBeenCalledWith("loop", [
      expect.objectContaining({ name: "shot.png" }),
    ]);
    expect(onSubmit).toHaveBeenCalledWith([
      { index: 0, text: "login" },
      { index: 1, text: "![shot.png](/api/v1/repos/loop/attachments/7)" },
    ]);
    unmount();
  });

  it("has nothing to submit until a question is answered", async () => {
    const onSubmit = vi.fn();
    const { container, unmount } = await render(
      [
        { index: 0, text: "login", auto: true },
        { index: 2, text: "yes", auto: true },
      ],
      onSubmit,
    );
    expect(buttonNamed(container, "Submit answers").disabled).toBe(true);
    unmount();
  });
});
