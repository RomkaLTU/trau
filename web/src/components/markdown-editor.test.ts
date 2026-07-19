// @vitest-environment happy-dom
import { Editor } from "@tiptap/core";
import Image from "@tiptap/extension-image";
import { Markdown } from "@tiptap/markdown";
import StarterKit from "@tiptap/starter-kit";
import { afterEach, describe, expect, it } from "vitest";

import type { UploadedAttachment } from "@/lib/attachments";

import { insertImages } from "./markdown-editor";

function attachment(id: number, filename: string): UploadedAttachment {
  return {
    id,
    url: `/api/v1/repos/testrepo/attachments/${id}`,
    filename,
    mime_type: "image/png",
  };
}

function ref(id: number, filename: string): string {
  return `![${filename}](/api/v1/repos/testrepo/attachments/${id})`;
}

describe("insertImages", () => {
  let editor: Editor;

  function newEditor(content = ""): Editor {
    editor = new Editor({
      extensions: [
        StarterKit,
        Image,
        Markdown.configure({ markedOptions: { gfm: true } }),
      ],
      content,
      contentType: "markdown",
    });
    return editor;
  }

  afterEach(() => editor.destroy());

  it("keeps earlier images when inserting one at a time", () => {
    const e = newEditor();
    insertImages(e, [attachment(18, "first.png")]);
    insertImages(e, [attachment(19, "second.png")]);
    const markdown = e.getMarkdown();
    expect(markdown).toContain(ref(18, "first.png"));
    expect(markdown).toContain(ref(19, "second.png"));
    expect(markdown.indexOf(ref(18, "first.png"))).toBeLessThan(
      markdown.indexOf(ref(19, "second.png")),
    );
  });

  it("inserts a batch in the order it was given", () => {
    const e = newEditor();
    insertImages(e, [attachment(1, "a.png"), attachment(2, "b.png")]);
    const markdown = e.getMarkdown();
    expect(markdown.indexOf(ref(1, "a.png"))).toBeLessThan(
      markdown.indexOf(ref(2, "b.png")),
    );
  });

  it("inserts at a drop position without replacing existing content", () => {
    const e = newEditor("already here");
    insertImages(e, [attachment(3, "dropped.png")], 1);
    const markdown = e.getMarkdown();
    expect(markdown).toContain(ref(3, "dropped.png"));
    expect(markdown).toContain("already here");
  });
});
