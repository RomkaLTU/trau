import { useEffect, useImperativeHandle, useRef, type Ref } from "react";
import type { Editor } from "@tiptap/core";
import { EditorContent, useEditor, useEditorState } from "@tiptap/react";
import StarterKit from "@tiptap/starter-kit";
import { TableKit } from "@tiptap/extension-table";
import { Placeholder } from "@tiptap/extensions";
import { Markdown } from "@tiptap/markdown";
import { Bold, Code, Italic, List, ListOrdered, Table } from "lucide-react";

import { cn } from "@/lib/utils";

export interface MarkdownEditorHandle {
  getMarkdown: () => string;
  clearContent: () => void;
}

export function MarkdownEditor({
  ref,
  placeholder,
  disabled = false,
  defaultValue = "",
  autoFocus = false,
  onChange,
  onEnter,
  className,
  contentClassName,
  editorClassName,
}: {
  ref?: Ref<MarkdownEditorHandle>;
  placeholder: string;
  disabled?: boolean;
  defaultValue?: string;
  autoFocus?: boolean;
  onChange?: (markdown: string) => void;
  onEnter?: () => void;
  className?: string;
  contentClassName?: string;
  editorClassName?: string;
}) {
  const placeholderRef = useRef(placeholder);
  placeholderRef.current = placeholder;
  const editorRef = useRef<Editor | null>(null);
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;
  const onEnterRef = useRef(onEnter);
  onEnterRef.current = onEnter;

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
    onUpdate: ({ editor: e }) => onChangeRef.current?.(e.getMarkdown()),
    editorProps: {
      attributes: {
        class: cn("min-h-9 px-3 py-2 text-sm outline-none", editorClassName),
      },
      handleKeyDown: (_view, event) => {
        if (event.key !== "Enter" || event.isComposing) return false;
        const submit = onEnterRef.current;
        if (!submit) return false;
        if (event.shiftKey) {
          return (
            editorRef.current?.commands.first(({ commands }) => [
              () => commands.newlineInCode(),
              () => commands.splitListItem("listItem"),
            ]) ?? false
          );
        }
        submit();
        return true;
      },
    },
  });
  editorRef.current = editor;

  useImperativeHandle(ref, () => ({
    getMarkdown: () => editorRef.current?.getMarkdown() ?? "",
    clearContent: () => {
      editorRef.current?.commands.clearContent(true);
    },
  }));

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
    <div
      className={cn(
        "composer-editor min-w-0 rounded-md border bg-transparent focus-within:ring-2 focus-within:ring-ring/50",
        disabled && "opacity-50",
        className,
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
      <EditorContent
        editor={editor}
        className={cn("max-h-48 overflow-y-auto", contentClassName)}
      />
    </div>
  );
}
