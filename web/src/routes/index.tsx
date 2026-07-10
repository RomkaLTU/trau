import { createFileRoute } from '@tanstack/react-router'

import { Eyebrow } from '@/components/trau/eyebrow'
import {
  LaunchActions,
  OverviewBoard,
  PulseStrip,
} from '@/components/trau/overview'
import { instancesQueryOptions } from '@/lib/instances'
import { reposQueryOptions } from '@/lib/runs'

export const Route = createFileRoute('/')({
  component: Overview,
  loader: ({ context }) =>
    Promise.all([
      context.queryClient.ensureQueryData(instancesQueryOptions),
      context.queryClient.ensureQueryData(reposQueryOptions),
    ]),
})

function Overview() {
  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-col gap-4 md:flex-row md:items-end md:justify-between">
        <div className="flex flex-col gap-2">
          <Eyebrow glyph="active" className="text-teal">
            OVERVIEW
          </Eyebrow>
          <h1 className="text-balance text-2xl font-semibold tracking-tight text-foreground">
            What trau is doing, and what needs you
          </h1>
        </div>
        <LaunchActions />
      </header>

      <PulseStrip />

      <OverviewBoard />
    </div>
  )
}
