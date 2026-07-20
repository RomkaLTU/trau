// @vitest-environment happy-dom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { describe, expect, it } from "vitest";

import { Markdown, markdownImageSources, parseBlocks } from "./markdown";

(
  globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true;

describe("parseBlocks", () => {
  it("parses a GFM table into header and rows", () => {
    const md = [
      "| col a | col b |",
      "| ----- | ----- |",
      "| val 1 | val 2 |",
      "| val 3 |       |",
    ].join("\n");
    expect(parseBlocks(md)).toEqual([
      {
        kind: "table",
        header: ["col a", "col b"],
        rows: [
          ["val 1", "val 2"],
          ["val 3", ""],
        ],
      },
    ]);
  });

  it("ends the preceding paragraph where a table starts", () => {
    const md = "intro\n| a | b |\n| --- | --- |\n| 1 | 2 |";
    expect(parseBlocks(md)).toEqual([
      { kind: "paragraph", text: "intro" },
      { kind: "table", header: ["a", "b"], rows: [["1", "2"]] },
    ]);
  });

  it("treats a pipe row without a delimiter line as a paragraph", () => {
    expect(parseBlocks("before\n| just | text |")).toEqual([
      { kind: "paragraph", text: "before | just | text |" },
    ]);
  });

  it("parses quote lines into a nested quote block and a rule into a rule", () => {
    expect(parseBlocks("> quoted\n> more\n\n---")).toEqual([
      { kind: "quote", blocks: [{ kind: "paragraph", text: "quoted more" }] },
      { kind: "rule" },
    ]);
  });
});

describe("markdownImageSources", () => {
  it("lists the image urls a body embeds and ignores plain links", () => {
    const md =
      "![one](https://a.test/1.png)\n\ntext [link](https://a.test/x)\n\n![](/api/v1/repos/r/attachments/4)";
    expect(markdownImageSources(md)).toEqual([
      "https://a.test/1.png",
      "/api/v1/repos/r/attachments/4",
    ]);
  });
});

async function render(
  container: HTMLElement,
  props: Parameters<typeof Markdown>[0],
) {
  const root = createRoot(container);
  await act(async () => {
    root.render(createElement(Markdown, props));
  });
  return () => root.unmount();
}

describe("Markdown", () => {
  it("renders a GFM table as an HTML table, not literal text", async () => {
    const md = "| col a | col b |\n| ----- | ----- |\n| val 1 | val 2 |";
    const container = document.createElement("div");
    const unmount = await render(container, { children: md });
    expect(container.querySelector("table")).not.toBeNull();
    expect(container.querySelectorAll("th")).toHaveLength(2);
    expect(container.querySelectorAll("td")).toHaveLength(2);
    expect(container.textContent).not.toContain("|");
    unmount();
  });

  it("renders an external image instead of literal markdown", async () => {
    const container = document.createElement("div");
    const unmount = await render(container, {
      children: "before ![a shot](https://pics.test/shot.png) after",
    });
    const img = container.querySelector("img");
    expect(img?.getAttribute("src")).toBe("https://pics.test/shot.png");
    expect(img?.getAttribute("alt")).toBe("a shot");
    expect(img?.getAttribute("loading")).toBe("lazy");
    expect(container.textContent).not.toContain("![");
    unmount();
  });

  it("rewrites a tracker image to the hub attachment it maps to", async () => {
    const container = document.createElement("div");
    const unmount = await render(container, {
      children: "![shot](https://uploads.linear.app/abc/shot.png)",
      urlMap: {
        "https://uploads.linear.app/abc/shot.png":
          "/api/v1/repos/loop/attachments/7",
      },
    });
    expect(container.querySelector("img")?.getAttribute("src")).toBe(
      "/api/v1/repos/loop/attachments/7",
    );
    unmount();
  });

  it("shows a placeholder for an unmapped tracker image", async () => {
    const container = document.createElement("div");
    const unmount = await render(container, {
      children: "![shot](https://uploads.linear.app/abc/shot.png)",
    });
    expect(container.querySelector("img")).toBeNull();
    expect(container.textContent).toContain("shot");
    expect(container.textContent).not.toContain("![");
    unmount();
  });

  it("renders a hub attachment url directly", async () => {
    const container = document.createElement("div");
    const unmount = await render(container, {
      children: "![](/api/v1/repos/loop/attachments/3)",
    });
    expect(container.querySelector("img")?.getAttribute("src")).toBe(
      "/api/v1/repos/loop/attachments/3",
    );
    unmount();
  });

  it("linkifies an external link and marks it noopener", async () => {
    const container = document.createElement("div");
    const unmount = await render(container, {
      children: "see [the docs](https://trau.sh/docs) for more",
    });
    const link = container.querySelector("a");
    expect(link?.getAttribute("href")).toBe("https://trau.sh/docs");
    expect(link?.textContent).toBe("the docs");
    expect(link?.getAttribute("rel")).toBe("noopener noreferrer");
    expect(link?.getAttribute("target")).toBe("_blank");
    unmount();
  });

  it("renders italics, blockquotes and rules instead of literal syntax", async () => {
    const container = document.createElement("div");
    const unmount = await render(container, {
      children: "*em* and ***both***\n\n> quoted\n\n---",
    });
    expect(container.querySelector("em")?.textContent).toBe("em");
    expect(container.querySelector("strong em")?.textContent).toBe("both");
    expect(container.querySelector("blockquote")?.textContent).toBe("quoted");
    expect(container.querySelector("hr")).not.toBeNull();
    expect(container.textContent).not.toContain("*");
    expect(container.textContent).not.toContain(">");
    expect(container.textContent).not.toContain("---");
    unmount();
  });

  it("keeps asterisks in plain arithmetic text literal", async () => {
    const container = document.createElement("div");
    const unmount = await render(container, { children: "2 * 3 * 4" });
    expect(container.querySelector("em")).toBeNull();
    expect(container.textContent).toBe("2 * 3 * 4");
    unmount();
  });

  it("leaves link syntax inside code spans alone", async () => {
    const container = document.createElement("div");
    const unmount = await render(container, {
      children: "run `[a](b)` verbatim",
    });
    expect(container.querySelector("a")).toBeNull();
    expect(container.querySelector("code")?.textContent).toBe("[a](b)");
    unmount();
  });
});
