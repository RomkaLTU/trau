import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
} from "@/components/ui/select";

// GrillModelSelect is the interview's provider/model control: a compact
// `<provider> · <model>` trigger over the provider's catalog. The provider is fixed
// to whatever the hub spawns, so it reads as a label qualifying the model rather
// than a second choice. An empty model is the provider CLI's own default.
export function GrillModelSelect({
  provider,
  model,
  options,
  label,
  disabled,
  onChange,
}: {
  provider: string;
  model: string;
  options: readonly string[];
  label: string;
  disabled?: boolean;
  onChange: (model: string) => void;
}) {
  return (
    <Select
      value={model}
      onValueChange={onChange}
      disabled={disabled || options.length === 0}
    >
      <SelectTrigger
        size="sm"
        className="h-7 gap-1 border-none bg-transparent px-2 font-mono text-xs text-muted-foreground shadow-none dark:bg-transparent"
        aria-label={label}
      >
        {provider} · {model || "default"}
      </SelectTrigger>
      <SelectContent align="end">
        {options.map((m) => (
          <SelectItem key={m} value={m} className="font-mono text-xs">
            {m}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
