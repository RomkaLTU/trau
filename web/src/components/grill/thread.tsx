import { useEffect } from "react";
import { AlertTriangle, Play, RotateCw } from "lucide-react";

import { BannerRow } from "@/components/grill/banners";
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
  outcomePayload,
  questionPayload,
  type GrillBanner,
  type GrillMessage,
  type GrillSession,
  type PendingAnswer,
  type StreamingReply,
} from "@/lib/grill";
import { cn } from "@/lib/utils";

// Hosts key the conversation on the session id, so switching queue rows unmounts the
// thread outright. Parking the message each session was reading here is what survives
// that remount and lets the reader come back to where they were reading.
const anchors = new Map<string, string>();

export function GrillThread({
  session,
  messages,
  hydrated,
  pending,
  streaming,
  stalled,
  onRetry,
  onDiscard,
  onResume,
}: {
  session: GrillSession;
  messages: GrillMessage[];
  hydrated: boolean;
  pending: PendingAnswer[];
  streaming: StreamingReply;
  stalled: GrillBanner | null;
  onRetry: (id: string) => void;
  onDiscard: (id: string) => void;
  onResume?: () => void;
}) {
  return (
    <MessageScrollerProvider autoScroll>
      <MessageScroller className="flex-1">
        <Viewport sessionId={session.id}>
          <MessageScrollerContent className="gap-5 px-4 py-4">
            {messages.map((m) => (
              <MessageScrollerItem key={m.id} messageId={m.id}>
                <MessageRow message={m} />
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
                <ThinkingRow text={streaming.holed ? "" : streaming.text} />
              </MessageScrollerItem>
            )}
            {hydrated && stalled && (
              <MessageScrollerItem messageId="stalled">
                <StalledNote banner={stalled} onResume={onResume} />
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
  children,
}: {
  sessionId: string;
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
    <MessageScrollerViewport aria-label="Interview transcript">
      {children}
    </MessageScrollerViewport>
  );
}

function MessageRow({ message }: { message: GrillMessage }) {
  switch (message.kind) {
    case "question":
      return <AgentBubble>{questionPayload(message).text}</AgentBubble>;
    case "answer":
      return <UserBubble>{answerText(message)}</UserBubble>;
    // The seed idea of an authoring session rides as an info message; render it as
    // the user's opening turn so the conversation reads from the top. A system info
    // message is hub bookkeeping (a model switch), not a turn, so it reads as a
    // notice line rather than a bubble.
    case "info":
      if (message.role === "user") {
        return <UserBubble>{answerText(message)}</UserBubble>;
      }
      if (message.role === "system") {
        return <SystemNote>{answerText(message)}</SystemNote>;
      }
      return <AgentBubble>{answerText(message)}</AgentBubble>;
    case "outcome":
      return <OutcomeProposal outcome={outcomePayload(message)} />;
    default:
      return null;
  }
}

function AgentBubble({ children }: { children: React.ReactNode }) {
  return (
    <Message align="start">
      <MessageContent>
        <Eyebrow>interview agent</Eyebrow>
        <Bubble
          variant="outline"
          align="start"
          className="max-w-[72ch] *:data-[slot=bubble-content]:bg-secondary/50"
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
  children,
  className,
}: {
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <Message align="end">
      <MessageContent>
        <Eyebrow>you</Eyebrow>
        <Bubble variant="default" align="end" className={cn("max-w-[56ch]", className)}>
          <BubbleContent className="whitespace-pre-wrap">
            {children}
          </BubbleContent>
        </Bubble>
      </MessageContent>
    </Message>
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
    <MessageHeader className="font-mono tracking-wide lowercase">
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
    return <UserBubble className="opacity-60">{pending.text}</UserBubble>;
  }
  return (
    <div className="flex flex-col gap-1.5">
      <UserBubble className="*:data-[slot=bubble-content]:bg-fail/15 *:data-[slot=bubble-content]:text-foreground">
        {pending.text}
      </UserBubble>
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
function ThinkingRow({ text }: { text: string }) {
  return (
    <AgentBubble>
      <span className="shimmer">{text === "" ? "Thinking" : text}</span>{" "}
      <span className="cursor-block text-teal" aria-hidden="true">
        ▌
      </span>
    </AgentBubble>
  );
}

function StalledNote({
  banner,
  onResume,
}: {
  banner: GrillBanner;
  onResume?: () => void;
}) {
  return (
    <div className="flex flex-col items-start gap-2.5">
      <BannerRow banner={banner} />
      {onResume && (
        <Button variant="outline" size="sm" onClick={onResume}>
          <Play />
          Resume session
        </Button>
      )}
    </div>
  );
}
