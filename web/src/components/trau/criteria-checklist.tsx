import { type CriterionStatus, type VerdictCriterion } from '@/lib/rundetail'
import { cn } from '@/lib/utils'

// Amber "?" keeps unverified off the green tick: a criterion nobody could
// observe must never skim as one that passed.
const MARKS: Record<CriterionStatus, { glyph: string; tone: string }> = {
  satisfied: { glyph: '✓', tone: 'text-done' },
  violated: { glyph: '✗', tone: 'text-fail' },
  unverified: { glyph: '?', tone: 'text-warn' },
}

// The no-rubric path leaves the agent's grades ungated on the Go side, so a
// status outside the union reaches us verbatim and reads as unverified.
function gradeOf(status: CriterionStatus): CriterionStatus {
  return status in MARKS ? status : 'unverified'
}

export function CriteriaChecklist({
  criteria,
}: {
  criteria: VerdictCriterion[]
}) {
  return (
    <ul className="flex flex-col gap-1.5 border-t border-border/60 pt-3">
      {criteria.map((c, i) => {
        const status = gradeOf(c.status)
        const mark = MARKS[status]
        return (
          <li key={i} className="flex items-start gap-2.5">
            <span
              aria-hidden="true"
              className={cn('mt-0.5 font-mono text-sm', mark.tone)}
            >
              {mark.glyph}
            </span>
            <span className="text-pretty font-sans text-sm leading-relaxed text-muted-foreground">
              <span className="sr-only">{status}: </span>
              {c.text}
              {c.note && <span className="text-faint"> — {c.note}</span>}
            </span>
          </li>
        )
      })}
    </ul>
  )
}
