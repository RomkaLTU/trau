import { createFileRoute } from '@tanstack/react-router'

import { Backlog } from '@/components/trau/backlog'
import { Eyebrow } from '@/components/trau/eyebrow'

export const Route = createFileRoute('/backlog')({
  component: BacklogPage,
})

function BacklogPage() {
  return (
    <div className="flex flex-col gap-8">
      <header className="flex flex-col gap-2">
        <Eyebrow glyph="idle" className="text-info">
          BACKLOG BOARD
        </Eyebrow>
        <h1 className="text-balance text-2xl font-semibold tracking-tight text-foreground">
          Backlog
        </h1>
        <p className="text-pretty text-sm leading-relaxed text-muted-foreground">
          Browse the Active repo's full tracker backlog — every ticket, grouped
          by status. Editing stays in Linear/Jira.
        </p>
      </header>

      <Backlog />
    </div>
  )
}
