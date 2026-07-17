import { useEffect, useRef } from "react";
import type { Editor } from "@tiptap/core";
import { EditorContent, useEditor, useEditorState } from "@tiptap/react";
import StarterKit from "@tiptap/starter-kit";
import { TableKit } from "@tiptap/extension-table";
import { Placeholder } from "@tiptap/extensions";
import { Markdown } from "@tiptap/markdown";
import {
  Bold,
  Code,
  Italic,
  List,
  ListOrdered,
  Loader2,
  Send,
  Table,
} from "lucide-react";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export function Composer({
  placeholder,
  disabled,
  submitting,
  onSend,
  defaultValue = "",
  autoFocus = false,
}: {
  placeholder: string;
  disabled: boolean;
  submitting: boolean;
  onSend: (text: string) => void;
  defaultValue?: string;
  autoFocus?: boolean;
}) {
  const placeholderRef = useRef(placeholder);
  placeholderRef.current = placeholder;
  const editorRef = useRef<Editor | null>(null);

  const send = () => {
    const ed = editorRef.current;
    if (!ed || disabled) return;
    const text = ed.getMarkdown().trim();
    if (text === "") return;
    onSend(text);
    ed.commands.clearContent(true);
  };

  const editor = useEditor({
    extensions: [
      StarterKit,
      TableKit,
      Markdown.configure({ markedOptions: { gfm: true } }),
      Placeholder.configure({
        placeholder: () => placeholderRef.current,
        showOnlyWhenEditable: false,
      }),
    ],
    content: defaultValue,
    contentType: "markdown",
    autofocus: autoFocus ? "end" : false,
    editable: !disabled,
    editorProps: {
      attributes: { class: "min-h-9 px-3 py-2 text-sm outline-none" },
      handleKeyDown: (_view, event) => {
        if (event.key !== "Enter" || event.isComposing) return false;
        if (event.shiftKey) {
          return (
            editorRef.current?.commands.first(({ commands }) => [
              () => commands.newlineInCode(),
              () => commands.splitListItem("listItem"),
            ]) ?? false
          );
        }
        send();
        return true;
      },
    },
  });
  editorRef.current = editor;

  useEffect(() => {
    editor?.setEditable(!disabled);
  }, [editor, disabled]);

  // The placeholder only repaints on a transaction; nudge one when it changes.
  useEffect(() => {
    if (editor?.isEmpty) editor.view.dispatch(editor.state.tr);
  }, [editor, placeholder]);

  const state = useEditorState({
    editor,
    selector: ({ editor: e }) => ({
      empty: !e || e.getMarkdown().trim() === "",
      bold: e?.isActive("bold") ?? false,
      italic: e?.isActive("italic") ?? false,
      code: e?.isActive("code") ?? false,
      bulletList: e?.isActive("bulletList") ?? false,
      orderedList: e?.isActive("orderedList") ?? false,
      table: e?.isActive("table") ?? false,
    }),
  });

  const tools = [
    {
      icon: Bold,
      label: "Bold",
      active: state?.bold ?? false,
      run: () => editor?.chain().focus().toggleBold().run(),
    },
    {
      icon: Italic,
      label: "Italic",
      active: state?.italic ?? false,
      run: () => editor?.chain().focus().toggleItalic().run(),
    },
    {
      icon: Code,
      label: "Code",
      active: state?.code ?? false,
      run: () => editor?.chain().focus().toggleCode().run(),
    },
    {
      icon: List,
      label: "Bullet list",
      active: state?.bulletList ?? false,
      run: () => editor?.chain().focus().toggleBulletList().run(),
    },
    {
      icon: ListOrdered,
      label: "Ordered list",
      active: state?.orderedList ?? false,
      run: () => editor?.chain().focus().toggleOrderedList().run(),
    },
    {
      icon: Table,
      label: state?.table ? "Remove table" : "Insert table",
      active: state?.table ?? false,
      run: () =>
        state?.table
          ? editor?.chain().focus().deleteTable().run()
          : editor
              ?.chain()
              .focus()
              .insertTable({ rows: 3, cols: 3, withHeaderRow: true })
              .run(),
    },
  ];

  return (
    <div className="flex items-end gap-2">
      <div
        className={cn(
          "composer-editor min-w-0 flex-1 rounded-md border bg-transparent focus-within:ring-2 focus-within:ring-ring/50",
          disabled && "opacity-50",
        )}
      >
        <div className="flex items-center gap-0.5 border-b px-1.5 py-1">
          {tools.map((tool) => (
            <button
              key={tool.label}
              type="button"
              tabIndex={-1}
              aria-label={tool.label}
              aria-pressed={tool.active}
              disabled={disabled}
              onMouseDown={(e) => e.preventDefault()}
              onClick={tool.run}
              className={cn(
                "inline-flex size-6 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground disabled:pointer-events-none disabled:opacity-50",
                tool.active && "bg-accent text-accent-foreground",
              )}
            >
              <tool.icon className="size-3.5" />
            </button>
          ))}
        </div>
        <EditorContent editor={editor} className="max-h-48 overflow-y-auto" />
      </div>
      <Button
        size="sm"
        onClick={send}
        disabled={disabled || (state?.empty ?? true)}
      >
        {submitting ? <Loader2 className="animate-spin" /> : <Send />}
        Send
      </Button>
    </div>
  );
}
