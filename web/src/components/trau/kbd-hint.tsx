import { cn } from '@/lib/utils'

// KbdHint renders a key, or a key sequence in press order, as the chip the
// sidebar's ⌘K affordance already uses.
export function KbdHint({
  keys,
  className,
}: {
  keys: readonly string[]
  className?: string
}) {
  return (
    <span
      data-slot="kbd-hint"
      className={cn('inline-flex shrink-0 items-center gap-1', className)}
    >
      {keys.map((key, index) => (
        <kbd
          key={`${key}-${index}`}
          className="rounded border border-border px-1 py-0.5 font-mono text-[0.65rem] text-muted-foreground"
        >
          {key}
        </kbd>
      ))}
    </span>
  )
}
