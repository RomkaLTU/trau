import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'

import { Eyebrow, NoticeBanner, type CheckpointNotice } from '@/components/trau'
import { RunLedger } from '@/components/trau/run-ledger'
import { standardTitle, usePageTitle } from '@/lib/page-title'
import { reposQueryOptions } from '@/lib/runs'

export const Route = createFileRoute('/runs')({
  component: Runs,
  loader: ({ context }) => context.queryClient.ensureQueryData(reposQueryOptions),
})

function Runs() {
  usePageTitle(standardTitle('Runs'))
  const [notice, setNotice] = useState<CheckpointNotice | null>(null)

  return (
    <div className="flex flex-col gap-8">
      <header className="flex flex-col gap-2">
        <Eyebrow glyph="partial" className="text-info">
          RUNS
        </Eyebrow>
        <h1 className="text-balance text-2xl font-semibold tracking-tight text-foreground">
          Runs
        </h1>
        <p className="text-pretty text-sm leading-relaxed text-muted-foreground">
          Every tracked run, newest first. Blocked runs surface at the top.
        </p>
      </header>

      {notice && <NoticeBanner notice={notice} onDismiss={() => setNotice(null)} />}

      <RunLedger onNotice={setNotice} />
    </div>
  )
}
