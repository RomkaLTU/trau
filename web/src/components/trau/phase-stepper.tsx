import { cn } from '@/lib/utils'
import type { Step, StepState } from '@/lib/steps'

const STEP_GLYPH: Record<StepState, string> = {
  done: '✓',
  active: '●',
  todo: '○',
  fail: '✗',
}

const STEP_TEXT: Record<StepState, string> = {
  done: 'text-done',
  active: 'text-teal',
  todo: 'text-faint',
  fail: 'text-fail',
}

export function PhaseStepper({
  steps,
  subLabel,
  compact = false,
}: {
  steps: Step[]
  subLabel?: string
  compact?: boolean
}) {
  if (compact) {
    return (
      <span className="inline-flex items-center gap-2 font-mono text-xs">
        <span className="inline-flex items-center gap-1" aria-hidden="true">
          {steps.map((step) => (
            <span key={step.label} className={STEP_TEXT[step.state]}>
              {STEP_GLYPH[step.state]}
            </span>
          ))}
        </span>
        {subLabel && <span className="text-muted-foreground">{subLabel}</span>}
        <span className="sr-only">
          {steps.map((step) => `${step.label} ${step.state}`).join(', ')}
        </span>
      </span>
    )
  }

  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-2 font-mono text-xs">
        {steps.map((step, i) => (
          <span key={step.label} className="inline-flex items-center gap-2">
            <span
              className={cn(
                'inline-flex items-center gap-1',
                STEP_TEXT[step.state],
              )}
            >
              <span aria-hidden="true">{STEP_GLYPH[step.state]}</span>
              {step.label}
              {step.state === 'active' && (
                <span className="cursor-block text-teal" aria-hidden="true">
                  ▍
                </span>
              )}
            </span>
            {i < steps.length - 1 && (
              <span className="text-faint" aria-hidden="true">
                →
              </span>
            )}
          </span>
        ))}
      </div>
      {subLabel && (
        <span className="font-mono text-xs text-muted-foreground">{subLabel}</span>
      )}
    </div>
  )
}
