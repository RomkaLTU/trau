import { useEffect, useReducer, useRef, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";

import { BannerRow } from "@/components/grill/banners";
import { Composer } from "@/components/grill/composer";
import {
  OutcomeProposal,
  OutcomeReview,
} from "@/components/grill/outcome-review";
import { QuestionCard } from "@/components/grill/question-card";
import {
  answerGrill,
  answerText,
  grillBanner,
  grillDetailQueryOptions,
  grillReducer,
  grillStreamURL,
  isAwaitingAnswer,
  latestOutcome,
  outcomePayload,
  pendingQuestion,
  questionPayload,
  type GrillMessage,
  type GrillSession,
} from "@/lib/grill";
import { streamSSE } from "@/lib/sse";
import { cn } from "@/lib/utils";

export type StreamStatus = "connecting" | "live" | "error";

// GrillStatus is what the conversation knows and its host frame has to render:
// the stream's connection state, the authoritative session behind the thread, and
// the thread itself — the messages arrive over SSE, so a host that read them back
// over GET would trail the conversation it is framing.
export interface GrillStatus {
  stream: StreamStatus;
  session: GrillSession;
  messages: GrillMessage[];
}

// GrillConversation is the chat itself — thread, pending question, and outcome
// review — with no frame of its own: it hydrates the session over GET, follows it
// over SSE, and reports both to the host through onStatus. Hosts supply the chrome
// and lay it out as a flex column.
export function GrillConversation({
  repo,
  initial,
  onStatus,
  onApplied,
}: {
  repo: string;
  initial: GrillSession;
  onStatus?: (status: GrillStatus) => void;
  onApplied?: () => void;
}) {
  const detail = useQuery(grillDetailQueryOptions(initial.id));
  const [state, dispatch] = useReducer(grillReducer, undefined, () => ({
    session: initial,
    live: false,
    messages: [],
  }));
  const [status, setStatus] = useState<StreamStatus>("connecting");
  const bottom = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (detail.data) dispatch({ type: "hydrate", detail: detail.data });
  }, [detail.data]);

  useEffect(() => {
    setStatus("connecting");
    const close = streamSSE(grillStreamURL(initial.id), {
      onOpen: () => setStatus("live"),
      onError: () => setStatus("error"),
      onMessage: ({ event, data }) => {
        let parsed: unknown;
        try {
          parsed = JSON.parse(data);
        } catch {
          return;
        }
        if (event === "state")
          dispatch({ type: "state", session: parsed as GrillSession });
        else if (event === "message")
          dispatch({ type: "message", message: parsed as GrillMessage });
      },
    });
    return () => close();
  }, [initial.id]);

  const { session, messages } = state;
  const pending = pendingQuestion(messages);
  const outcomeMsg = latestOutcome(messages);
  const reviewing =
    outcomeMsg !== null &&
    (session.state === "finished" || session.state === "applied");

  const answer = useMutation({
    mutationFn: (text: string) => answerGrill(session.id, text),
    onSuccess: (res) => {
      dispatch({ type: "message", message: res.message });
      dispatch({ type: "state", session: res.session });
    },
  });

  useEffect(() => {
    onStatus?.({ stream: status, session, messages });
  }, [status, session, messages]);

  useEffect(() => {
    bottom.current?.scrollIntoView({ block: "end" });
  }, [messages, session.state, answer.isPending]);

  const awaiting = isAwaitingAnswer(session.state);
  const banner = grillBanner(session);
  const showBanner =
    banner !== null && banner.tone !== "thinking" && !reviewing;
  const showFooter =
    reviewing || showBanner || awaiting || answer.error !== null;

  return (
    <>
      <div className="flex-1 overflow-y-auto px-4 py-4">
        <div className="flex flex-col gap-3">
          {messages.map((m) => {
            if (pending && m.id === pending.id) return null;
            if (reviewing && outcomeMsg && m.id === outcomeMsg.id) return null;
            return <MessageRow key={m.id} message={m} />;
          })}
          {session.state === "running" && <ThinkingRow />}
          <div ref={bottom} />
        </div>
      </div>

      {showFooter && (
        <div className="flex flex-col gap-3 border-t p-4">
          {reviewing && outcomeMsg ? (
            <OutcomeReview
              repo={repo}
              issueId={session.issue_id ?? ""}
              session={session}
              outcome={outcomePayload(outcomeMsg)}
              onSession={(next) => dispatch({ type: "state", session: next })}
              onApplied={onApplied}
            />
          ) : (
            <>
              {showBanner && <BannerRow banner={banner} />}
              {awaiting &&
                (pending ? (
                  <QuestionCard
                    question={questionPayload(pending)}
                    disabled={answer.isPending}
                    onAnswer={(text) => answer.mutate(text)}
                  />
                ) : (
                  <Composer
                    placeholder="Reply to resume…"
                    disabled={answer.isPending}
                    submitting={answer.isPending}
                    onSend={(text) => answer.mutate(text)}
                    defaultValue={
                      session.state === "stalled" ? lastAnswer(messages) : ""
                    }
                  />
                ))}
              {answer.error && (
                <p className="text-xs text-destructive">
                  {(answer.error as Error).message}
                </p>
              )}
            </>
          )}
        </div>
      )}
    </>
  );
}

function MessageRow({ message }: { message: GrillMessage }) {
  switch (message.kind) {
    case "question":
      return <Bubble role="agent">{questionPayload(message).text}</Bubble>;
    case "answer":
      return <Bubble role="user">{answerText(message)}</Bubble>;
    // The seed idea of an authoring session rides as an info message; render it as
    // the user's opening turn so the conversation reads from the top.
    case "info":
      return (
        <Bubble role={message.role === "user" ? "user" : "agent"}>
          {answerText(message)}
        </Bubble>
      );
    case "outcome":
      return <OutcomeProposal outcome={outcomePayload(message)} />;
    default:
      return null;
  }
}

function Bubble({
  role,
  children,
}: {
  role: "agent" | "user";
  children: React.ReactNode;
}) {
  const user = role === "user";
  return (
    <div className={cn("flex", user ? "justify-end" : "justify-start")}>
      <div
        className={cn(
          "max-w-[85%] whitespace-pre-wrap rounded-2xl px-3 py-2 text-sm",
          user
            ? "rounded-br-sm bg-primary text-primary-foreground"
            : "rounded-bl-sm bg-muted text-foreground",
        )}
      >
        {children}
      </div>
    </div>
  );
}

function ThinkingRow() {
  return (
    <div className="flex justify-start">
      <span className="inline-flex items-center gap-2 rounded-2xl rounded-bl-sm bg-muted px-3 py-2 text-sm text-muted-foreground">
        <Loader2 className="size-3.5 animate-spin" />
        Thinking…
      </span>
    </div>
  );
}

function lastAnswer(messages: GrillMessage[]): string {
  for (let i = messages.length - 1; i >= 0; i--) {
    if (messages[i].kind === "answer") return answerText(messages[i]);
  }
  return "";
}
