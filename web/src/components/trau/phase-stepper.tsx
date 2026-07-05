import { cn } from '@/lib/utils'
import type { RunPhaseState, RunPhaseStep } from '@/lib/runlive'

const PHASE_GLYPH: Record<RunPhaseState, string> = {
  done: '✓',
  active: '●',
  todo: '○',
  fail: '✗',
}

const PHASE_TEXT: Record<RunPhaseState, string> = {
  done: 'text-done',
  active: 'text-teal',
  todo: 'text-faint',
  fail: 'text-fail',
}

export function PhaseStepper({ steps }: { steps: RunPhaseStep[] }) {
  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-2 font-mono text-xs">
      {steps.map((step, i) => (
        <span key={step.label} className="inline-flex items-center gap-2">
          <span
            className={cn(
              'inline-flex items-center gap-1',
              PHASE_TEXT[step.state],
            )}
          >
            <span aria-hidden="true">{PHASE_GLYPH[step.state]}</span>
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
  )
}
