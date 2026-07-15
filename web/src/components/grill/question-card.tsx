import { Composer } from "@/components/grill/composer";
import { Button } from "@/components/ui/button";
import type { QuestionPayload } from "@/lib/grill";

export function QuestionCard({
  question,
  disabled,
  onAnswer,
}: {
  question: QuestionPayload;
  disabled: boolean;
  onAnswer: (text: string) => void;
}) {
  return (
    <div className="flex flex-col gap-3 rounded-lg border bg-card p-3">
      <p className="whitespace-pre-wrap text-sm text-foreground">
        {question.text}
      </p>
      {question.options.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {question.options.map((opt) => (
            <Button
              key={opt}
              variant="outline"
              size="sm"
              disabled={disabled}
              onClick={() => onAnswer(opt)}
            >
              {opt}
            </Button>
          ))}
        </div>
      )}
      {question.allow_free_text && (
        <Composer
          placeholder={
            question.options.length > 0
              ? "Or type your own answer…"
              : "Type your answer…"
          }
          disabled={disabled}
          submitting={disabled}
          onSend={onAnswer}
        />
      )}
    </div>
  );
}
