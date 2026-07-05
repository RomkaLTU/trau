import { createFileRoute } from '@tanstack/react-router'

import { Eyebrow } from '@/components/trau/eyebrow'
import { RunOnce } from '@/components/trau/run-once'
import { reposQueryOptions } from '@/lib/runs'

export const Route = createFileRoute('/run-once')({
  component: RunOncePage,
  loader: ({ context }) => context.queryClient.ensureQueryData(reposQueryOptions),
})

function RunOncePage() {
  return (
    <div className="flex flex-col gap-8">
      <header className="flex flex-col gap-2">
        <Eyebrow glyph="action" className="text-primary">
          SOLO MODE
        </Eyebrow>
        <h1 className="text-balance text-2xl font-semibold tracking-tight text-foreground">
          Run once
        </h1>
        <p className="text-pretty text-sm leading-relaxed text-muted-foreground">
          Drive one ticket through build → verify → merge.
        </p>
      </header>

      <RunOnce />
    </div>
  )
}
