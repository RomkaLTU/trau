import { useEffect, useReducer, useRef, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";

import { BannerRow } from "@/components/grill/banners";
import { Composer } from "@/components/grill/composer";
import { OutcomeReview } from "@/components/grill/outcome-review";
import { Suggestions } from "@/components/grill/suggestions";
import { GrillThread } from "@/components/grill/thread";
import {
  answerGrill,
  canCompose,
  composerPlaceholder,
  grillBanner,
  grillDetailQueryOptions,
  grillReducer,
  grillStreamURL,
  lastAnswer,
  latestOutcome,
  NO_REPLY,
  outcomePayload,
  pendingQuestion,
  questionPayload,
  type GrillDelta,
  type GrillMessage,
  type GrillSession,
} from "@/lib/grill";
import { streamSSE } from "@/lib/sse";

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

// GrillConversation is the chat itself — thread, suggestions, composer, and outcome
// review — with no frame of its own: it hydrates the session over GET, follows it
// over SSE, and reports both to the host through onStatus. Hosts supply the chrome
// and lay it out as a flex column.
export function GrillConversation({
  repo,
  initial,
  onStatus,
  onApplied,
  onDiscarded,
}: {
  repo: string;
  initial: GrillSession;
  onStatus?: (status: GrillStatus) => void;
  onApplied?: () => void;
  onDiscarded?: () => void;
}) {
  const detail = useQuery(grillDetailQueryOptions(initial.id));
  const [state, dispatch] = useReducer(grillReducer, undefined, () => ({
    session: initial,
    live: false,
    hydrated: false,
    messages: [],
    pending: [],
    streaming: NO_REPLY,
  }));
  const [status, setStatus] = useState<StreamStatus>("connecting");
  const [followUp, setFollowUp] = useState(false);
  const nextSend = useRef(0);

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
        else if (event === "delta")
          dispatch({ type: "delta", delta: parsed as GrillDelta });
      },
    });
    return () => close();
  }, [initial.id]);

  const { session, messages, pending, hydrated, streaming } = state;
  const asked = pendingQuestion(messages);
  const question = asked ? questionPayload(asked) : null;
  const outcomeMsg = latestOutcome(messages);
  const reviewing =
    outcomeMsg !== null &&
    (session.state === "finished" || session.state === "applied");

  // Each proposal is reviewed on its own terms, so a superseding outcome closes the
  // composer the one before it reopened. A follow-up is only sendable while the
  // proposal stands: applying settles the session and the hub stops taking answers.
  useEffect(() => setFollowUp(false), [outcomeMsg?.id]);
  const asking = followUp && session.state === "finished";

  // The mutation carries the optimistic twin's id so a failure lands on the bubble it
  // belongs to; the echo it resolves with retires that twin through the reducer.
  const answer = useMutation({
    mutationFn: ({ text }: { id: string; text: string }) =>
      answerGrill(session.id, text),
    onSuccess: (res) => {
      dispatch({ type: "message", message: res.message });
      dispatch({ type: "state", session: res.session });
    },
    onError: (_err, { id, text }) => dispatch({ type: "send-failed", id, text }),
  });

  useEffect(() => {
    onStatus?.({ stream: status, session, messages });
  }, [status, session, messages]);

  const send = (text: string) => {
    const id = `pending-${nextSend.current++}`;
    dispatch({ type: "send", id, text });
    answer.mutate({ id, text });
  };

  const retry = (id: string) => {
    const held = pending.find((p) => p.id === id);
    if (!held) return;
    dispatch({ type: "send-retry", id });
    answer.mutate({ id, text: held.text });
  };

  // The stalled banner rides in the thread, where its Resume button sits next to the
  // turn it died on; every other tone belongs above the composer.
  const banner = grillBanner(session);
  const stalled = banner?.showResume ? banner : null;
  const showBanner =
    banner !== null && stalled === null && banner.tone !== "thinking" && !reviewing;

  // A session that stalled on its opening turn has no answer to replay, so the box
  // reopens rather than stranding the user behind a Resume button with nothing to send.
  const resume = stalled ? lastAnswer(messages) : "";
  const answering = canCompose(session.state) || (stalled !== null && resume === "");
  const freeText = question?.allow_free_text ?? true;
  const sending = answer.isPending;

  return (
    <>
      <GrillThread
        session={session}
        messages={messages}
        hydrated={hydrated}
        pending={pending}
        streaming={streaming}
        stalled={stalled}
        onRetry={retry}
        onDiscard={(id) => dispatch({ type: "send-discard", id })}
        onResume={resume === "" ? undefined : () => send(resume)}
      />

      <div className="flex flex-col gap-3 border-t p-4">
        {reviewing && outcomeMsg ? (
          <>
            <OutcomeReview
              repo={repo}
              issueId={session.issue_id ?? ""}
              session={session}
              outcome={outcomePayload(outcomeMsg)}
              onSession={(next) => dispatch({ type: "state", session: next })}
              onApplied={onApplied}
              onDiscarded={onDiscarded}
              onAskFollowUp={asking ? undefined : () => setFollowUp(true)}
            />
            {asking && (
              <Composer
                placeholder="Ask a follow-up…"
                disabled={sending}
                submitting={sending}
                onSend={send}
              />
            )}
          </>
        ) : (
          <>
            {showBanner && <BannerRow banner={banner} />}
            {question && question.options.length > 0 && (
              <Suggestions
                options={question.options}
                recommended={question.recommended}
                why={question.why}
                disabled={sending || !answering}
                onPick={send}
              />
            )}
            <Composer
              placeholder={
                !answering
                  ? composerPlaceholder(session.state)
                  : freeText
                    ? "Type your answer…"
                    : "Pick one of the answers above…"
              }
              disabled={!answering || !freeText || sending}
              submitting={sending}
              onSend={send}
            />
          </>
        )}
      </div>
    </>
  );
}
