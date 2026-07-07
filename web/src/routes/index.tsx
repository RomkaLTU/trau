import { createFileRoute } from '@tanstack/react-router'

import { useActiveRepo } from '@/components/trau/active-repo'
import { EmptyState } from '@/components/trau/empty-state'
import { Eyebrow } from '@/components/trau/eyebrow'
import {
  LiveLoops,
  NeedsAttention,
  QuickLaunch,
  StatTiles,
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
  const { repo } = useActiveRepo()

  return (
    <div className="flex flex-col gap-8">
      <header className="flex flex-col gap-2">
        <Eyebrow glyph="active" className="text-teal">
          OVERVIEW
        </Eyebrow>
        <h1 className="text-balance text-2xl font-semibold tracking-tight text-foreground">
          {repo
            ? `What trau is doing in ${repo}`
            : 'What trau is doing, and what needs you'}
        </h1>
      </header>

      {repo ? (
        <>
          <StatTiles />

          <div className="grid grid-cols-1 gap-8 lg:grid-cols-5">
            <div className="flex flex-col gap-2 lg:col-span-3">
              <Eyebrow glyph="active">LIVE LOOPS</Eyebrow>
              <LiveLoops />
            </div>

            <div className="flex flex-col gap-6 lg:col-span-2">
              <div className="flex flex-col gap-2">
                <Eyebrow glyph="warn">NEEDS ATTENTION</Eyebrow>
                <NeedsAttention />
              </div>
              <div className="flex flex-col gap-2">
                <Eyebrow glyph="action">QUICK LAUNCH</Eyebrow>
                <QuickLaunch />
              </div>
            </div>
          </div>
        </>
      ) : (
        <EmptyState message="No repos yet. Register a repo to check it out and start driving loops from here." />
      )}
    </div>
  )
}
