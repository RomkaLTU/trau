import { createFileRoute } from '@tanstack/react-router'

import { Eyebrow } from '@/components/trau/eyebrow'
import { Loop } from '@/components/trau/loop'
import { ProjectScopeGate } from '@/components/trau/project-scope-gate'
import { instancesQueryOptions } from '@/lib/instances'

export const Route = createFileRoute('/loop')({
  component: LoopPage,
  loader: ({ context }) =>
    context.queryClient.ensureQueryData(instancesQueryOptions),
})

function LoopPage() {
  return (
    <div className="flex flex-col gap-8">
      <header className="flex flex-col gap-2">
        <Eyebrow glyph="active" className="text-teal">
          LOOP MODE
        </Eyebrow>
        <h1 className="text-balance text-2xl font-semibold tracking-tight text-foreground">
          Loop
        </h1>
        <p className="text-pretty text-sm leading-relaxed text-muted-foreground">
          Build one ordered queue of tickets and epics, then run it top to
          bottom — one ticket at a time.
        </p>
      </header>

      <ProjectScopeGate action="start a loop">
        <Loop />
      </ProjectScopeGate>
    </div>
  )
}
