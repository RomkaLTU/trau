import { createFileRoute } from '@tanstack/react-router'

import { Eyebrow } from '@/components/trau/eyebrow'
import { Loop } from '@/components/trau/loop'
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
          Graze a repo's ready queue, or drive an epic — one ticket at a time.
        </p>
      </header>

      <Loop />
    </div>
  )
}
