import { createFileRoute } from '@tanstack/react-router'

import { Eyebrow } from '@/components/trau/eyebrow'
import { Queue } from '@/components/trau/queue'

export const Route = createFileRoute('/queue')({
  component: QueuePage,
})

function QueuePage() {
  return (
    <div className="flex flex-col gap-8">
      <header className="flex flex-col gap-2">
        <Eyebrow glyph="idle" className="text-info">
          EXECUTION QUEUE
        </Eyebrow>
        <h1 className="text-balance text-2xl font-semibold tracking-tight text-foreground">
          Queue
        </h1>
        <p className="text-pretty text-sm leading-relaxed text-muted-foreground">
          Work registered for execution in the Active repo, in order. A ticket
          runs once; an epic carries its sub-issues. The hub drains the queue one
          run at a time — remove a pending item to drop it before it runs.
        </p>
      </header>

      <Queue />
    </div>
  )
}
