import { useEffect } from "react";
import { AlertTriangle, Play, RotateCw, Square } from "lucide-react";

import { ActivityFeed } from "@/components/grill/activity";
import { AnswerBody } from "@/components/grill/answer-body";
import { AutoAcceptBadge } from "@/components/grill/auto-accept";
import { BannerRow, ErrorNote } from "@/components/grill/banners";
import { OutcomeProposal } from "@/components/grill/outcome-review";
import { Bubble, BubbleContent } from "@/components/ui/bubble";
import { Button } from "@/components/ui/button";
import { Message, MessageContent, MessageHeader } from "@/components/ui/message";
import {
  MessageScroller,
  MessageScrollerButton,
  MessageScrollerContent,
  MessageScrollerItem,
  MessageScrollerProvider,
  MessageScrollerViewport,
  useMessageScroller,
  useMessageScrollerScrollable,
  useMessageScrollerVisibility,
} from "@/components/ui/message-scroller";
import {
  answerText,
  isAutoAnswer,
  isRoundAnswer,
  outcomePayload,
  questionPayload,
  roundAnswers,
  roundQuestions,
  type GrillActivity,
  type GrillBanner,
  type GrillMessage,
  type GrillMode,
  type GrillSession,
  type PendingAnswer,
  type RoundAnswer,
  type RoundQuestion,
  type StreamingReply,
} from "@/lib/grill";
import { cn } from "@/lib/utils";

// Hosts key the conversation on the session id, so switching queue rows unmounts the
// thread outright. Parking the message each session was reading here is what survives
// that remount and lets the reader come back to where they were reading.
const anchors = new Map<string, string>();

const THREAD_AGENT: Record<GrillMode, string> = {
  interview: "interview agent",
  research: "research agent",
  fix: "diagnosis agent",
};

const THREAD_TRANSCRIPT: Record<GrillMode, string> = {
  interview: "Interview transcript",
  research: "Research transcript",
  fix: "Propose fix transcript",
};

export function GrillThread({
  session,
  messages,
  hydrated,
  pending,
  streaming,
  activity,
  stalled,
  stopping,
  stopError,
  resuming,
  resumeError,
  onRetry,
  onDiscard,
  onResume,
  onStop,
}: {
  session: GrillSession;
  messages: GrillMessage[];
  hydrated: boolean;
  pending: PendingAnswer[];
  streaming: StreamingReply;
  activity: GrillActivity[];
  stalled: GrillBanner | null;
  stopping?: boolean;
  stopError?: string;
  resuming?: boolean;
  resumeError?: string;
  onRetry: (id: string) => void;
  onDiscard: (id: string) => void;
  onResume?: () => void;
  onStop?: () => void;
}) {
  const agent = THREAD_AGENT[session.mode ?? "interview"];
  const transcript = THREAD_TRANSCRIPT[session.mode ?? "interview"];
  return (
    <MessageScrollerProvider autoScroll>
      <MessageScroller className="flex-1">
        <Viewport
          sessionId={session.id}
          label={transcript}
        >
          <MessageScrollerContent className="gap-5 px-4 py-4">
            {messages.map((m) => (
              <MessageScrollerItem key={m.id} messageId={m.id}>
                <MessageRow message={m} agent={agent} />
              </MessageScrollerItem>
            ))}
            {pending.map((p) => (
              <MessageScrollerItem key={p.id} messageId={p.id} scrollAnchor>
                <PendingRow
                  pending={p}
                  onRetry={() => onRetry(p.id)}
                  onDiscard={() => onDiscard(p.id)}
                />
              </MessageScrollerItem>
            ))}
            {/* A session knows it is running or stalled before its transcript arrives,
                but seeding these rows that early costs the reader their place: the jump
                Viewport asks for is only parked while the thread is still empty. */}
            {hydrated && session.state === "running" && (
              <MessageScrollerItem messageId="thinking">
                <ThinkingRow
                  agent={agent}
                  text={streaming.holed ? "" : streaming.text}
                  stopping={stopping}
                  stopError={stopError}
                  onStop={onStop}
                />
              </MessageScrollerItem>
            )}
            {hydrated && session.state === "running" && activity.length > 0 && (
              <MessageScrollerItem messageId="activity">
                <ActivityFeed items={activity} />
              </MessageScrollerItem>
            )}
            {hydrated && stalled && (
              <MessageScrollerItem messageId="stalled">
                <StalledNote
                  banner={stalled}
                  resuming={resuming}
                  resumeError={resumeError}
                  onResume={onResume}
                />
              </MessageScrollerItem>
            )}
          </MessageScrollerContent>
        </Viewport>
        <MessageScrollerButton />
      </MessageScroller>
    </MessageScrollerProvider>
  );
}

function Viewport({
  sessionId,
  label,
  children,
}: {
  sessionId: string;
  label: string;
  children: React.ReactNode;
}) {
  const { scrollToMessage } = useMessageScroller();
  const { end: awayFromEdge } = useMessageScrollerScrollable();
  const { visibleMessageIds } = useMessageScrollerVisibility();

  // Asking for the jump while the remounted thread is still empty parks it, which also
  // calls off the scroller's default scroll to the live edge: the transcript hydrates
  // straight onto the saved message rather than snapping to the bottom and back.
  useEffect(() => {
    const anchor = anchors.get(sessionId);
    if (anchor) scrollToMessage(anchor, { align: "start" });
  }, [scrollToMessage, sessionId]);

  useEffect(() => {
    const top = visibleMessageIds[0];
    if (!top) return;
    if (awayFromEdge) anchors.set(sessionId, top);
    else anchors.delete(sessionId);
  }, [awayFromEdge, sessionId, visibleMessageIds]);

  return (
    <MessageScrollerViewport aria-label={label}>
      {children}
    </MessageScrollerViewport>
  );
}

function MessageRow({
  message,
  agent,
}: {
  message: GrillMessage;
  agent: string;
}) {
  switch (message.kind) {
    case "question": {
      const round = roundQuestions(message);
      if (round) {
        return (
          <AgentBubble agent={agent}>
            <RoundTranscript round={round} answers={roundAnswers(message)} />
          </AgentBubble>
        );
      }
      return (
        <AgentBubble agent={agent}>{questionPayload(message).text}</AgentBubble>
      );
    }
    // The answer that closes a round is the whole set at once, which the round's own
    // cards above already show one by one; repeating it as a bubble says nothing new.
    case "answer":
      if (isRoundAnswer(message)) return null;
      return (
        <UserBubble text={answerText(message)} auto={isAutoAnswer(message)} />
      );
    case "interjection":
      return <UserBubble text={answerText(message)} interjected />;
    // The seed idea of an authoring session rides as an info message; render it as
    // the user's opening turn so the conversation reads from the top. A system info
    // message is hub bookkeeping (a model switch), not a turn, so it reads as a
    // notice line rather than a bubble.
    case "info":
      if (message.role === "user") {
        return <UserBubble text={answerText(message)} />;
      }
      if (message.role === "system") {
        return <SystemNote>{answerText(message)}</SystemNote>;
      }
      return <AgentBubble agent={agent}>{answerText(message)}</AgentBubble>;
    case "outcome":
      return <OutcomeProposal outcome={outcomePayload(message)} />;
    default:
      return null;
  }
}

// RoundTranscript is a round as the transcript reads it: the questions numbered as
// they were asked, each with the answer it got. An answer the hub took from the agent's
// own recommendation is badged, exactly as a single auto-accepted answer is.
function RoundTranscript({
  round,
  answers,
}: {
  round: RoundQuestion[];
  answers: RoundAnswer[];
}) {
  const settled = new Map(answers.map((a) => [a.index, a]));
  return (
    <div className="flex flex-col gap-2">
      {round.map((q, i) => {
        const answer = settled.get(i);
        return (
          <div key={`${i}-${q.text}`} className="flex gap-2">
            <span className="font-mono text-xs text-muted-foreground">
              {i + 1}.
            </span>
            <div className="flex min-w-0 flex-1 flex-col gap-1">
              <span className="whitespace-pre-wrap">{q.text}</span>
              {answer && (
                <span className="flex items-center gap-1.5 text-sm text-muted-foreground">
                  <span className="min-w-0 flex-1 whitespace-pre-wrap">
                    {answer.text}
                  </span>
                  {answer.auto && <AutoAcceptBadge />}
                </span>
              )}
            </div>
          </div>
        );
      })}
    </div>
  );
}

function AgentBubble({
  agent,
  children,
}: {
  agent: string;
  children: React.ReactNode;
}) {
  return (
    <Message align="start">
      <MessageContent>
        <Eyebrow>{agent}</Eyebrow>
        <Bubble
          variant="outline"
          align="start"
          className="max-w-[72ch] *:data-[slot=bubble-content]:bg-secondary"
        >
          <BubbleContent className="whitespace-pre-wrap">
            {children}
          </BubbleContent>
        </Bubble>
      </MessageContent>
    </Message>
  );
}

function UserBubble({
  text,
  auto,
  interjected,
  className,
}: {
  text: string;
  auto?: boolean;
  interjected?: boolean;
  className?: string;
}) {
  return (
    <Message align="end">
      <MessageContent>
        <Eyebrow>
          you
          {auto && <AutoAcceptBadge />}
          {interjected && <InterjectionBadge />}
        </Eyebrow>
        <Bubble variant="default" align="end" className={cn("max-w-[56ch]", className)}>
          <BubbleContent className="whitespace-pre-wrap">
            <AnswerBody text={text} />
          </BubbleContent>
        </Bubble>
      </MessageContent>
    </Message>
  );
}

function InterjectionBadge() {
  return (
    <span
      title="Interjected — sent while the agent was working, delivered at its next step"
      className="inline-flex shrink-0 items-center rounded-full border border-border bg-secondary/60 px-1.5 py-0.5 font-mono text-[0.65rem] uppercase tracking-wide text-muted-foreground"
    >
      interjected
    </span>
  );
}

function SystemNote({ children }: { children: React.ReactNode }) {
  return (
    <p className="py-0.5 text-center font-mono text-xs text-muted-foreground">
      {children}
    </p>
  );
}

function Eyebrow({ children }: { children: React.ReactNode }) {
  return (
    <MessageHeader className="gap-1.5 font-mono tracking-wide lowercase">
      {children}
    </MessageHeader>
  );
}

// A send that errored keeps its bubble and grows the recovery controls beneath it, so
// the text the user typed is never the thing that gets lost.
function PendingRow({
  pending,
  onRetry,
  onDiscard,
}: {
  pending: PendingAnswer;
  onRetry: () => void;
  onDiscard: () => void;
}) {
  if (!pending.failed) {
    return <UserBubble text={pending.text} className="opacity-60" />;
  }
  return (
    <div className="flex flex-col gap-1.5">
      <UserBubble
        text={pending.text}
        className="*:data-[slot=bubble-content]:bg-[color-mix(in_oklch,var(--fail)_15%,var(--background))] *:data-[slot=bubble-content]:text-foreground"
      />
      <div className="flex items-center justify-end gap-1 text-xs text-muted-foreground">
        <AlertTriangle className="size-3.5 text-fail" aria-hidden="true" />
        <span>Not sent.</span>
        <Button variant="link" size="sm" onClick={onRetry}>
          <RotateCw />
          Retry
        </Button>
        <Button
          variant="link"
          size="sm"
          onClick={onDiscard}
          className="text-muted-foreground"
        >
          Discard
        </Button>
      </div>
    </div>
  );
}

// The indicator is an agent bubble, not a lookalike, so the real message replaces it
// in the shell it already occupies rather than jolting the thread. text is the reply
// so far and grows in place under the same shimmer, reading as provisional until the
// stored message settles it; a turn that streams nothing keeps the bare word.
function ThinkingRow({
  agent,
  text,
  stopping,
  stopError,
  onStop,
}: {
  agent: string;
  text: string;
  stopping?: boolean;
  stopError?: string;
  onStop?: () => void;
}) {
  return (
    <div className="flex flex-col items-start gap-2">
      <AgentBubble agent={agent}>
        <span className="shimmer">{text === "" ? "Thinking" : text}</span>{" "}
        <span className="cursor-block text-teal" aria-hidden="true">
          ▌
        </span>
      </AgentBubble>
      {onStop && (
        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={onStop}
            disabled={stopping}
            title="End this turn and take the conversation back"
          >
            <Square />
            Stop
          </Button>
          {stopError && (
            <span className="text-[11px] text-fail">{stopError}</span>
          )}
        </div>
      )}
    </div>
  );
}

function StalledNote({
  banner,
  resuming,
  resumeError,
  onResume,
}: {
  banner: GrillBanner;
  resuming?: boolean;
  resumeError?: string;
  onResume?: () => void;
}) {
  return (
    <div className="flex flex-col items-start gap-2.5">
      <BannerRow banner={banner} />
      {onResume && (
        <Button
          variant="outline"
          size="sm"
          onClick={onResume}
          disabled={resuming}
          title="Restart the turn the agent stopped on"
        >
          <Play />
          Resume session
        </Button>
      )}
      {resumeError && <ErrorNote message={resumeError} />}
    </div>
  );
}
