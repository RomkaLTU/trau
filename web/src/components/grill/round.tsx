import { useMemo, useState, type ClipboardEvent } from "react";
import { toast } from "sonner";

import { AutoAcceptBadge } from "@/components/grill/auto-accept";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { uploadAttachments } from "@/lib/attachments";
import type { RoundAnswer, RoundQuestion } from "@/lib/grill";
import { cn } from "@/lib/utils";

// RoundForm is a round of independent questions as one form: a numbered card per
// question, answered in whatever order the reader likes and submitted once. It is the
// only surface that answers a round — a single line cannot settle a set — so the
// composer beside it steps back while one is open.
//
// Every question the hub has already settled renders read-only with the answer it got,
// which is how an auto-accept round shows the recommendations it took for itself and
// how a round the user stepped away from shows what they had already given.
export function RoundForm({
  repo,
  questions,
  answers,
  disabled,
  submitting,
  onSubmit,
}: {
  repo: string;
  questions: RoundQuestion[];
  answers: RoundAnswer[];
  disabled: boolean;
  submitting: boolean;
  onSubmit: (answers: RoundAnswer[]) => void;
}) {
  const settled = useMemo(
    () => new Map(answers.map((a) => [a.index, a])),
    [answers],
  );
  // The agent's recommendation opens as the answer, so a round of questions it already
  // has an opinion on is a read and a click rather than a form to fill in.
  const [draft, setDraft] = useState<Record<number, string>>(() =>
    questions.reduce<Record<number, string>>((acc, q, i) => {
      if (q.recommended) acc[i] = q.recommended;
      return acc;
    }, {}),
  );

  const open = questions.filter((_, i) => !settled.has(i));
  const filled = questions.filter((_, i) => !settled.has(i) && draft[i]?.trim());
  const submit = () => {
    const out = questions
      .map((_, i) => ({ index: i, text: (draft[i] ?? "").trim() }))
      .filter((a) => a.text !== "" && !settled.has(a.index));
    if (out.length > 0) onSubmit(out);
  };

  return (
    <div className="flex flex-col gap-2.5">
      <p className="text-xs text-muted-foreground">
        {open.length === questions.length
          ? `${questions.length} questions — answer them in any order.`
          : `${settled.size} of ${questions.length} answered — ${open.length} left for you.`}
      </p>
      {questions.map((q, i) => {
        const answer = settled.get(i);
        return (
          <div
            key={`${i}-${q.text}`}
            className={cn(
              "flex flex-col gap-2 rounded-md border p-3",
              answer && "border-dashed bg-muted/30",
            )}
          >
            <div className="flex gap-2">
              <span className="font-mono text-xs text-muted-foreground">
                {i + 1}.
              </span>
              <p className="min-w-0 flex-1 text-sm whitespace-pre-wrap text-foreground">
                {q.text}
              </p>
            </div>
            {answer ? (
              <p className="flex items-center gap-1.5 pl-6 text-sm text-muted-foreground">
                <span className="min-w-0 flex-1 whitespace-pre-wrap">
                  {answer.text}
                </span>
                {answer.auto && <AutoAcceptBadge />}
              </p>
            ) : (
              <RoundQuestionInput
                question={q}
                repo={repo}
                value={draft[i] ?? ""}
                disabled={disabled}
                onChange={(text) => setDraft((d) => ({ ...d, [i]: text }))}
                onAttach={(ref) =>
                  setDraft((d) => ({ ...d, [i]: withAttachment(d[i] ?? "", ref) }))
                }
              />
            )}
          </div>
        );
      })}
      <div className="flex items-center justify-end gap-3">
        {filled.length < open.length && (
          <span className="text-xs text-muted-foreground">
            {filled.length} of {open.length} answered
          </span>
        )}
        <Button
          type="button"
          disabled={disabled || submitting || filled.length === 0}
          onClick={submit}
        >
          {submitting ? "Sending…" : "Submit answers"}
        </Button>
      </div>
    </div>
  );
}

// withAttachment puts an uploaded image's reference on a line of its own under what the
// answer already says, so a paste adds to the answer instead of replacing it.
function withAttachment(text: string, ref: string): string {
  const body = text.replace(/\s+$/, "");
  return body === "" ? ref : `${body}\n\n${ref}`;
}

// RoundQuestionInput is one open question's controls: its options as a pick list, and
// the free-text box when the question takes one. Picking an option fills the answer in
// rather than sending it — the round goes back as a whole. A pasted image uploads and
// leaves its hub reference in the answer, which the hub materializes for the child on
// its way out, so a screenshot answers a round question as it answers a single one.
function RoundQuestionInput({
  question,
  repo,
  value,
  disabled,
  onChange,
  onAttach,
}: {
  question: RoundQuestion;
  repo: string;
  value: string;
  disabled: boolean;
  onChange: (text: string) => void;
  onAttach: (ref: string) => void;
}) {
  const paste = async (event: ClipboardEvent<HTMLTextAreaElement>) => {
    const files = Array.from(event.clipboardData?.files ?? []);
    if (files.length === 0) return;
    event.preventDefault();
    const toastId = toast.loading(
      files.length > 1 ? `Uploading ${files.length} images…` : "Uploading image…",
    );
    const { uploaded, errors } = await uploadAttachments(repo, files);
    toast.dismiss(toastId);
    uploaded.forEach((att) => onAttach(`![${att.filename}](${att.url})`));
    errors.forEach((message) => toast.error(message));
  };

  return (
    <div className="flex flex-col gap-1.5 pl-6">
      {question.options.map((opt) => (
        <div key={opt} className="flex flex-col gap-1">
          <Button
            type="button"
            variant={value === opt ? "default" : "outline"}
            aria-pressed={value === opt}
            disabled={disabled}
            onClick={() => onChange(opt)}
            className="h-auto w-full flex-col items-start gap-1.5 py-2 text-left whitespace-normal"
          >
            {opt === question.recommended && <Badge>Recommended</Badge>}
            <span>{opt}</span>
          </Button>
          {opt === question.recommended && question.why && (
            <p className="px-1 text-xs text-muted-foreground">{question.why}</p>
          )}
        </div>
      ))}
      {question.allow_free_text && (
        <Textarea
          rows={2}
          value={value}
          disabled={disabled}
          placeholder={
            question.options.length > 0
              ? "…or answer in your own words"
              : "Type your answer…"
          }
          onChange={(e) => onChange(e.target.value)}
          onPaste={(e) => void paste(e)}
        />
      )}
    </div>
  );
}
