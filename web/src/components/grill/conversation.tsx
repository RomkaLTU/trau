import { useEffect, useReducer, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, Sparkles } from "lucide-react";

import { BannerRow } from "@/components/grill/banners";
import { Composer } from "@/components/grill/composer";
import { OutcomeReview } from "@/components/grill/outcome-review";
import { ReportDocument } from "@/components/grill/report";
import { Suggestions } from "@/components/grill/suggestions";
import { GrillThread } from "@/components/grill/thread";
import { useOpenConversation } from "@/lib/open-conversation";
import {
  answerGrill,
  canCompose,
  composerPlaceholder,
  dropAwaiting,
  grillBanner,
  grillDetailQueryOptions,
  grillReducer,
  grillStreamURL,
  lastAnswer,
  latestOutcome,
  NO_ACTIVITY,
  NO_REPLY,
  outcomePayload,
  pendingQuestion,
  questionPayload,
  stopGrill,
  type GrillActivity,
  type GrillAppliedOutcome,
  type GrillDelta,
  type GrillMessage,
  type GrillSession,
} from "@/lib/grill";
import { useActivityShown } from "@/lib/grill-activity";
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

// GrillOutcomeVariant is how a host renders a finished session's proposal: the full
// approve-then-apply review, or — for a host too small to review one in, like the
// dock — a link handing it off to the surface that owns applying.
export type GrillOutcomeVariant = "review" | "link";

// GrillConversation is the chat itself — thread, suggestions, composer, and outcome
// review — with no frame of its own: it hydrates the session over GET, follows it
// over SSE, and reports both to the host through onStatus. Hosts supply the chrome
// and lay it out as a flex column.
export function GrillConversation({
  repo,
  initial,
  outcome = "review",
  activity = true,
  report = false,
  onStatus,
  onApplied,
  onDiscarded,
  onReview,
}: {
  repo: string;
  initial: GrillSession;
  outcome?: GrillOutcomeVariant;
  // A host with no room for the feed — the dock's 520px panel — turns it off outright;
  // everywhere else it follows the reader's own preference.
  activity?: boolean;
  // report gives a settled research session the pane as a document, with the turns
  // that produced it behind a disclosure. Only a host whose whole surface is the
  // report — the Research page — asks for it.
  report?: boolean;
  onStatus?: (status: GrillStatus) => void;
  onApplied?: (applied: GrillAppliedOutcome) => void;
  onDiscarded?: () => void;
  onReview?: () => void;
}) {
  useOpenConversation(initial.id);
  const queryClient = useQueryClient();
  const detail = useQuery(grillDetailQueryOptions(initial.id));
  const [state, dispatch] = useReducer(grillReducer, undefined, () => ({
    session: initial,
    live: false,
    hydrated: false,
    messages: [],
    pending: [],
    streaming: NO_REPLY,
    activity: NO_ACTIVITY,
  }));
  const [activityShown] = useActivityShown();
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
        else if (event === "activity")
          dispatch({ type: "activity", activity: parsed as GrillActivity });
      },
    });
    return () => close();
  }, [initial.id]);

  const { session, messages, pending, hydrated, streaming } = state;
  const asked = pendingQuestion(messages);
  const question = asked ? questionPayload(asked) : null;
  const outcomeMsg = latestOutcome(messages);
  const proposal = outcomeMsg ? outcomePayload(outcomeMsg) : null;
  const reviewing =
    outcomeMsg !== null &&
    (session.state === "finished" || session.state === "applied");
  // An applied report has nothing left to decide, so the document stands alone.
  const reporting = report && reviewing && proposal?.disposition === "research";
  const decided = reporting && session.state === "applied";

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
      // An interjection leaves the turn running, so replaying its state frame would
      // only reset the reply the thread is still streaming.
      if (res.message.kind !== "interjection")
        dispatch({ type: "state", session: res.session });
      dropAwaiting(queryClient, session.id);
    },
    onError: (_err, { id, text }) =>
      dispatch({ type: "send-failed", id, text }),
  });

  // The hub publishes the park it made on the session's own state frames, so the
  // reply is left undispatched: it is a snapshot of the moment the stop landed, and
  // replaying it would roll the thread back over whatever frame arrived since.
  const stop = useMutation({ mutationFn: () => stopGrill(session.id) });

  // A refused stop belongs to the turn it was aimed at; the next turn starts clean.
  useEffect(() => {
    if (session.state !== "running") stop.reset();
  }, [session.state]);

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
    banner !== null &&
    stalled === null &&
    banner.tone !== "thinking" &&
    !reviewing;

  // A session that stalled on its opening turn has no answer to replay, so the box
  // reopens rather than stranding the user behind a Resume button with nothing to send.
  const resume = stalled ? lastAnswer(messages) : "";
  const answering =
    canCompose(session.state) || (stalled !== null && resume === "");
  const freeText = question?.allow_free_text ?? true;
  const sending = answer.isPending;
  // A running turn takes typing as steering, not as an answer, so it reads its
  // prompt off the state rather than off the question.
  const placeholder =
    !answering || session.state === "running"
      ? composerPlaceholder(session.state)
      : freeText
        ? "Type your answer…"
        : "Pick one of the answers above…";

  const thread = (
    <GrillThread
      session={session}
      messages={messages}
      hydrated={hydrated}
      pending={pending}
      streaming={streaming}
      activity={
        activity && activityShown ? state.activity.items : NO_ACTIVITY.items
      }
      stalled={stalled}
      stopping={stop.isPending}
      stopError={stop.error?.message}
      onRetry={retry}
      onDiscard={(id) => dispatch({ type: "send-discard", id })}
      onResume={resume === "" ? undefined : () => send(resume)}
      onStop={() => stop.mutate()}
    />
  );

  return (
    <>
      {reporting && proposal ? (
        <div className="min-h-0 flex-1 overflow-y-auto">
          <ReportDocument
            session={session}
            outcome={proposal}
            warnings={session.apply_warnings ?? []}
          />
          <TranscriptDisclosure>{thread}</TranscriptDisclosure>
        </div>
      ) : (
        thread
      )}

      {!decided && (
        <div className="flex flex-col gap-3 border-t p-4">
          {reviewing && outcomeMsg && outcome === "link" ? (
            <ProposalLink onOpen={onReview} />
          ) : reviewing && proposal ? (
            <>
              <OutcomeReview
                repo={repo}
                issueId={session.issue_id ?? ""}
                session={session}
                outcome={proposal}
                reportShown={reporting}
                onSession={(next) => dispatch({ type: "state", session: next })}
                onApplied={onApplied}
                onDiscarded={onDiscarded}
                onAskFollowUp={asking ? undefined : () => setFollowUp(true)}
              />
              {asking && (
                <Composer
                  repo={repo}
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
                repo={repo}
                placeholder={placeholder}
                disabled={!answering || !freeText || sending}
                submitting={sending}
                onSend={send}
              />
            </>
          )}
        </div>
      )}
    </>
  );
}

// The thread is only mounted while the disclosure is open: it measures its own
// viewport to keep the reader's place, which a collapsed box gives it no room to do.
function TranscriptDisclosure({ children }: { children: React.ReactNode }) {
  const [open, setOpen] = useState(false);
  return (
    <details
      onToggle={(e) => setOpen(e.currentTarget.open)}
      className="mx-auto w-full max-w-[72ch] px-6 pb-8"
    >
      <summary className="cursor-pointer text-xs text-muted-foreground transition-colors hover:text-foreground">
        View research transcript
      </summary>
      {open && (
        <div className="mt-2 flex h-96 flex-col rounded-md border border-border">
          {children}
        </div>
      )}
    </details>
  );
}

// ProposalLink hands a ready proposal off to the Inbox's full surface.
function ProposalLink({ onOpen }: { onOpen?: () => void }) {
  return (
    <button
      type="button"
      onClick={onOpen}
      className="flex items-center gap-2.5 rounded-md border border-info/40 bg-info/5 px-3 py-2.5 text-left transition-colors hover:bg-info/10"
    >
      <Sparkles className="size-4 shrink-0 text-info" aria-hidden="true" />
      <span className="min-w-0 flex-1 text-sm font-medium text-foreground">
        Proposal ready — review in Inbox
      </span>
      <ArrowRight
        className="size-4 shrink-0 text-muted-foreground"
        aria-hidden="true"
      />
    </button>
  );
}
