import { useEffect, useRef, useState } from "react";
import { Terminal as Xterm } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";

import { TerminalCard } from "@/components/trau/terminal-card";
import { cn } from "@/lib/utils";
import { streamSSE } from "@/lib/sse";
import {
  decodeChunk,
  transcriptStreamURL,
  type TranscriptMeta,
  type TranscriptStatus,
} from "@/lib/transcripts";

const THEME = {
  background: "#14110e",
  foreground: "#efe9e3",
  cursor: "#14110e",
};

// Terminal renders a repo's live agent transcript in an embedded xterm.js. It
// holds one fetch-based SSE stream for the whole component lifetime — a single
// connection per selected repo, so switching phases never leaks connections into
// the browser's per-origin cap. The stream sizes the emulator from the meta
// frame's recorded PTY dimensions and clears it on a reset (a new phase or an
// in-place truncation) before the fresh bytes land. The server never closes the
// stream, so `live` is the caller's call on whether it's still streaming.
export function Terminal({
  repo,
  id,
  since,
  title,
  live = true,
  tall = false,
  className,
}: {
  repo: string;
  id?: string;
  since?: string;
  title?: string;
  live?: boolean;
  tall?: boolean;
  className?: string;
}) {
  const holder = useRef<HTMLDivElement>(null);
  const termRef = useRef<Xterm | null>(null);
  const followedID = useRef("");
  const [status, setStatus] = useState<TranscriptStatus>("connecting");

  useEffect(() => {
    if (!holder.current) return;
    const term = new Xterm({
      convertEol: false,
      cursorBlink: false,
      disableStdin: true,
      fontSize: 12,
      fontFamily:
        '"Geist Mono Variable", ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace',
      scrollback: 5000,
      theme: THEME,
    });
    term.open(holder.current);
    termRef.current = term;
    return () => {
      term.dispose();
      termRef.current = null;
    };
  }, []);

  useEffect(() => {
    const term = termRef.current;
    if (!term || !repo) return;

    followedID.current = "";
    term.reset();
    setStatus("connecting");

    const close = streamSSE(transcriptStreamURL(repo, id, since), {
      onOpen: () => setStatus("live"),
      onError: () => setStatus("error"),
      onMessage: ({ event, data }) => {
        if (event === "meta") {
          let meta: TranscriptMeta;
          try {
            meta = JSON.parse(data);
          } catch {
            return;
          }
          if (meta.id !== followedID.current) {
            term.reset();
            followedID.current = meta.id;
          }
          term.resize(Math.max(1, meta.cols), Math.max(1, meta.rows));
        } else if (event === "reset") {
          term.reset();
        } else {
          term.write(decodeChunk(data));
        }
      },
    });

    return () => close();
  }, [repo, id, since]);

  return (
    <TerminalCard
      title={title ?? `${repo} · ${id ?? "newest"}`}
      scanlines
      bodyClassName="p-0"
      className={cn(tall && "min-h-[55vh]", className)}
    >
      <div className="flex flex-col">
        <div
          ref={holder}
          className="overflow-auto bg-[#14110e] p-3"
          style={{ height: tall ? "60vh" : "32rem" }}
        />
        <StatusLine status={status} live={live} />
      </div>
    </TerminalCard>
  );
}

function StatusLine({
  status,
  live,
}: {
  status: TranscriptStatus;
  live: boolean;
}) {
  return (
    <div className="flex items-center border-t border-border px-4 py-2 font-mono text-xs">
      {status === "error" ? (
        <span className="inline-flex items-center gap-1.5 text-warn">
          <span aria-hidden="true">⚠</span>
          reconnecting…
        </span>
      ) : status === "connecting" ? (
        <span className="text-muted-foreground">connecting…</span>
      ) : live ? (
        <span className="inline-flex items-center gap-1.5 text-teal">
          <span className="cursor-block" aria-hidden="true">
            ▍
          </span>
          streaming
        </span>
      ) : (
        <span className="text-faint">
          stream ended — waiting for the next phase
        </span>
      )}
    </div>
  );
}
