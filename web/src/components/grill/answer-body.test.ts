// @vitest-environment happy-dom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { describe, expect, it } from "vitest";

import { AnswerBody } from "./answer-body";

(
  globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true;

async function render(text: string) {
  const container = document.createElement("div");
  const root = createRoot(container);
  await act(async () => {
    root.render(createElement(AnswerBody, { text }));
  });
  return { container, unmount: () => root.unmount() };
}

describe("AnswerBody", () => {
  it("shows a pasted attachment as an image, not the raw reference", async () => {
    const { container, unmount } = await render(
      "Looks like this:\n\n![shot.png](/api/v1/repos/acme/attachments/7)",
    );
    const img = container.querySelector("img");
    expect(img?.getAttribute("src")).toBe("/api/v1/repos/acme/attachments/7");
    expect(img?.getAttribute("alt")).toBe("shot.png");
    expect(container.textContent).toContain("Looks like this:");
    expect(container.textContent).not.toContain("![");
    unmount();
  });

  it("keeps an answer with no attachment as the text the user typed", async () => {
    const { container, unmount } = await render("**not** bold, just text");
    expect(container.querySelector("strong")).toBeNull();
    expect(container.textContent).toBe("**not** bold, just text");
    unmount();
  });
});
