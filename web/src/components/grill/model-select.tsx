import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
} from "@/components/ui/select";

// GrillModelSelect is the interview's model control: a compact trigger over the
// provider's catalog. Mid-session the provider is fixed, so it prefixes the model as
// a `<provider> · <model>` label; pre-start a separate provider picker owns the
// choice, so hideProvider drops the prefix. An empty model is the provider CLI's own
// default.
export function GrillModelSelect({
  provider,
  model,
  options,
  label,
  disabled,
  hideProvider,
  onChange,
}: {
  provider: string;
  model: string;
  options: readonly string[];
  label: string;
  disabled?: boolean;
  hideProvider?: boolean;
  onChange: (model: string) => void;
}) {
  const shown = model || "default";
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
        {hideProvider ? shown : `${provider} · ${shown}`}
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

// GrillProviderSelect is the pre-start provider picker: choosing a provider swaps the
// model list to its catalog. It only exists before a session starts, since the runner
// reads a per-provider stream contract and cannot switch provider mid-interview.
export function GrillProviderSelect({
  provider,
  providers,
  disabled,
  onChange,
}: {
  provider: string;
  providers: readonly string[];
  disabled?: boolean;
  onChange: (provider: string) => void;
}) {
  return (
    <Select
      value={provider}
      onValueChange={onChange}
      disabled={disabled || providers.length <= 1}
    >
      <SelectTrigger
        size="sm"
        className="h-7 gap-1 border-none bg-transparent px-2 font-mono text-xs text-muted-foreground shadow-none dark:bg-transparent"
        aria-label="Interview provider"
      >
        {provider}
      </SelectTrigger>
      <SelectContent align="end">
        {providers.map((p) => (
          <SelectItem key={p} value={p} className="font-mono text-xs">
            {p}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
