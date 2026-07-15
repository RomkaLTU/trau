import { Button } from "@/components/ui/button";

// Suggestions are the pending question's canned answers, stacked full-width above the
// composer. A grilling agent's options run to a sentence apiece, so they get their own
// rows rather than a wrapping chip strip that would clip or truncate them.
export function Suggestions({
  options,
  disabled,
  onPick,
}: {
  options: string[];
  disabled: boolean;
  onPick: (text: string) => void;
}) {
  return (
    <div className="flex flex-col gap-1.5">
      {options.map((opt) => (
        <Button
          key={opt}
          variant="outline"
          disabled={disabled}
          onClick={() => onPick(opt)}
          className="h-auto w-full justify-start py-2 text-left whitespace-normal"
        >
          {opt}
        </Button>
      ))}
    </div>
  );
}
