import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

// Suggestions are the pending question's canned answers, stacked full-width above the
// composer. A grilling agent's options run to a sentence apiece, so they get their own
// rows rather than a wrapping chip strip that would clip or truncate them. Option order
// is preserved — questions often reference options in sequence.
export function Suggestions({
  options,
  recommended,
  why,
  disabled,
  onPick,
}: {
  options: string[];
  recommended?: string;
  why?: string;
  disabled: boolean;
  onPick: (text: string) => void;
}) {
  return (
    <div className="flex flex-col gap-1.5">
      {options.map((opt) =>
        opt === recommended ? (
          <div key={opt} className="flex flex-col gap-1">
            <Button
              variant="outline"
              disabled={disabled}
              onClick={() => onPick(opt)}
              className="h-auto w-full flex-col items-start gap-1.5 border-primary py-2 text-left whitespace-normal"
            >
              <Badge>Recommended</Badge>
              <span>{opt}</span>
            </Button>
            {why && <p className="px-1 text-xs text-muted-foreground">{why}</p>}
          </div>
        ) : (
          <Button
            key={opt}
            variant="outline"
            disabled={disabled}
            onClick={() => onPick(opt)}
            className="h-auto w-full justify-start py-2 text-left whitespace-normal"
          >
            {opt}
          </Button>
        ),
      )}
    </div>
  );
}
