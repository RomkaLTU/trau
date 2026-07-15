import {
  stepIndex,
  WIZARD_STEPS,
  type WizardStepId,
} from '@/lib/onboarding'
import { cn } from '@/lib/utils'

type StepVisual = 'done' | 'active' | 'todo'

const GLYPH: Record<StepVisual, string> = {
  done: '✓',
  active: '●',
  todo: '○',
}

const TEXT: Record<StepVisual, string> = {
  done: 'text-done',
  active: 'text-primary',
  todo: 'text-faint',
}

export function WizardStepper({
  current,
  maxReached,
  onNavigate,
}: {
  current: WizardStepId
  maxReached: WizardStepId
  onNavigate: (step: WizardStepId) => void
}) {
  const currentIdx = stepIndex(current)
  const maxIdx = stepIndex(maxReached)

  return (
    <nav aria-label="Onboarding steps">
      <ol className="flex flex-row flex-wrap gap-x-4 gap-y-1 lg:flex-col lg:gap-y-3">
        {WIZARD_STEPS.map((step, i) => {
          const visual: StepVisual =
            i === currentIdx ? 'active' : i < currentIdx ? 'done' : 'todo'
          const reachable = i <= maxIdx && i !== currentIdx

          return (
            <li key={step.id} className="relative flex items-center gap-2">
              <button
                type="button"
                disabled={!reachable}
                aria-current={i === currentIdx ? 'step' : undefined}
                onClick={() => reachable && onNavigate(step.id)}
                className={cn(
                  'flex items-center gap-2 font-mono text-xs',
                  reachable ? 'cursor-pointer hover:text-foreground' : 'cursor-default',
                  TEXT[visual],
                )}
              >
                <span aria-hidden="true" className="w-3 text-center">
                  {GLYPH[visual]}
                </span>
                <span>{step.label}</span>
              </button>
            </li>
          )
        })}
      </ol>
    </nav>
  )
}
