import { useState } from 'react'
import { ArrowRight, Plus } from 'lucide-react'

import { Button } from '@/components/ui/button'
import type { FindingState, MemberRepo } from '@/lib/onboarding'
import { cn } from '@/lib/utils'
import { Callout, Hint } from './ui'

const FINDING: Record<FindingState, { glyph: string; color: string }> = {
  ok: { glyph: '✓', color: 'text-done' },
  warn: { glyph: '⚠', color: 'text-warn' },
  missing: { glyph: '○', color: 'text-faint' },
  info: { glyph: '●', color: 'text-info' },
}

export function StepDetect({
  members,
  onAddPath,
  onBack,
  onContinue,
}: {
  members: MemberRepo[]
  onAddPath: () => void
  onBack: () => void
  onContinue: () => void
}) {
  const [selected, setSelected] = useState(0)
  const member = members[selected] ?? members[0]
  const inspection = member.inspection
  const hasWarnings = inspection.findings.some((f) => f.state === 'warn')

  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-col gap-1.5">
        <h2 className="font-mono text-base text-foreground">Here's what trau found</h2>
        <Hint className="font-mono">{inspection.path}</Hint>
      </div>

      {members.length > 1 && (
        <div className="flex flex-wrap items-center gap-1.5">
          {members.map((m, i) => (
            <button
              key={m.root}
              type="button"
              onClick={() => setSelected(i)}
              className={cn(
                'rounded border px-2 py-0.5 font-mono text-xs',
                i === selected
                  ? 'border-primary/60 bg-primary/10 text-foreground'
                  : 'border-border text-muted-foreground hover:text-foreground',
              )}
            >
              {m.repo}
            </button>
          ))}
        </div>
      )}

      {hasWarnings && (
        <Callout tone="warn" title="This config would break sync as-is">
          Resolve the flagged items on the next step, or the seed sync pulls from the wrong place.
        </Callout>
      )}
      {inspection.has_trau_ini && !hasWarnings && (
        <Callout tone="info" title="Existing config detected — the wizard is pre-filled">
          Values below come from the repo's <span className="font-mono">.trau.ini</span>. Stored
          secrets stay put unless you replace them.
        </Callout>
      )}

      <ul className="divide-y divide-border overflow-hidden rounded-md border border-border">
        {inspection.findings.map((f) => {
          const style = FINDING[f.state]
          return (
            <li key={f.label} className="flex flex-col gap-1 px-3 py-2.5">
              <div className="flex items-baseline gap-2">
                <span className="flex w-44 shrink-0 items-baseline gap-1.5 font-mono text-[0.65rem] uppercase tracking-[0.15em] text-muted-foreground">
                  <span className={style.color} aria-hidden="true">
                    {style.glyph}
                  </span>
                  {f.label}
                </span>
                <span className={cn('font-mono text-sm', style.color)}>{f.value}</span>
              </div>
              {f.detail && (
                <p className="pl-6 font-sans text-xs leading-relaxed text-muted-foreground">
                  {f.detail}
                </p>
              )}
            </li>
          )
        })}
      </ul>

      {members.length > 1 && (
        <Hint>
          The tracker step configures the whole project — every member inherits the same
          keys. The essentials step gives every member its own base branch; the ready label
          and epic flow are project-wide, so every member inherits those too.
        </Hint>
      )}

      <div className="flex items-center justify-between">
        <div className="flex items-center gap-1">
          <Button type="button" variant="ghost" onClick={onBack}>
            Back
          </Button>
          <Button type="button" variant="ghost" onClick={onAddPath}>
            <Plus className="size-4" />
            Add path
          </Button>
        </div>
        <Button type="button" onClick={onContinue}>
          Set up tracker <ArrowRight className="size-4" />
        </Button>
      </div>
    </div>
  )
}
