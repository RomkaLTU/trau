import { useState } from "react";
import { Loader2, Send } from "lucide-react";

import { Button } from "@/components/ui/button";

export function Composer({
  placeholder,
  disabled,
  submitting,
  onSend,
  defaultValue = "",
}: {
  placeholder: string;
  disabled: boolean;
  submitting: boolean;
  onSend: (text: string) => void;
  defaultValue?: string;
}) {
  const [text, setText] = useState(defaultValue);
  const send = () => {
    const trimmed = text.trim();
    if (trimmed === "" || disabled) return;
    onSend(trimmed);
    setText("");
  };
  return (
    <div className="flex items-end gap-2">
      <textarea
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
            e.preventDefault();
            send();
          }
        }}
        placeholder={placeholder}
        rows={1}
        disabled={disabled}
        className="max-h-32 min-h-9 flex-1 resize-none rounded-md border bg-transparent px-3 py-2 text-sm outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-50"
      />
      <Button
        size="sm"
        onClick={send}
        disabled={disabled || text.trim() === ""}
      >
        {submitting ? <Loader2 className="animate-spin" /> : <Send />}
        Send
      </Button>
    </div>
  );
}
