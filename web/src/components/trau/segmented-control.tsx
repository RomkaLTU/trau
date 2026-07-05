import { cn } from '@/lib/utils'

export interface SegmentOption<T extends string | number> {
  value: T
  label: string
}

export function SegmentedControl<T extends string | number>({
  options,
  value,
  onChange,
  className,
  'aria-label': ariaLabel,
}: {
  options: readonly SegmentOption<T>[]
  value: T
  onChange: (value: T) => void
  className?: string
  'aria-label'?: string
}) {
  return (
    <div
      role="radiogroup"
      aria-label={ariaLabel}
      className={cn(
        'inline-flex w-fit rounded-md border border-border bg-input p-0.5',
        className,
      )}
    >
      {options.map((option) => {
        const active = option.value === value
        return (
          <button
            key={String(option.value)}
            type="button"
            role="radio"
            aria-checked={active}
            onClick={() => onChange(option.value)}
            className={cn(
              'rounded-[calc(var(--radius)-6px)] px-3 py-1 font-mono text-xs transition-colors',
              active
                ? 'bg-primary text-primary-foreground'
                : 'text-muted-foreground hover:text-foreground',
            )}
          >
            {option.label}
          </button>
        )
      })}
    </div>
  )
}

export function WindowPicker<T extends string | number>({
  windows,
  value,
  onChange,
  className,
}: {
  windows: readonly T[]
  value: T
  onChange: (value: T) => void
  className?: string
}) {
  return (
    <SegmentedControl
      aria-label="Time window"
      options={windows.map((w) => ({ value: w, label: String(w) }))}
      value={value}
      onChange={onChange}
      className={className}
    />
  )
}
