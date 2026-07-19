import { useRef, useState } from "react";
import { Loader2, Send } from "lucide-react";

import {
  MarkdownEditor,
  type MarkdownEditorHandle,
} from "@/components/markdown-editor";
import { Button } from "@/components/ui/button";

export function Composer({
  repo,
  placeholder,
  disabled,
  submitting,
  onSend,
  defaultValue = "",
  autoFocus = false,
}: {
  repo: string;
  placeholder: string;
  disabled: boolean;
  submitting: boolean;
  onSend: (text: string) => void;
  defaultValue?: string;
  autoFocus?: boolean;
}) {
  const editorRef = useRef<MarkdownEditorHandle | null>(null);
  const [empty, setEmpty] = useState(defaultValue.trim() === "");

  const send = () => {
    const ed = editorRef.current;
    if (!ed || disabled) return;
    const text = ed.getMarkdown().trim();
    if (text === "") return;
    onSend(text);
    ed.clearContent();
  };

  return (
    <div className="flex items-end gap-2">
      <MarkdownEditor
        ref={editorRef}
        repo={repo}
        className="flex-1"
        placeholder={placeholder}
        disabled={disabled}
        defaultValue={defaultValue}
        autoFocus={autoFocus}
        onChange={(markdown) => setEmpty(markdown.trim() === "")}
        onEnter={send}
      />
      <Button size="sm" onClick={send} disabled={disabled || empty}>
        {submitting ? <Loader2 className="animate-spin" /> : <Send />}
        Send
      </Button>
    </div>
  );
}
