import { createFileRoute } from '@tanstack/react-router'

import { Eyebrow, ProjectScopeGate, useActiveRepo } from '@/components/trau'
import { AppURLsSection } from '@/components/trau/app-urls-panel'
import { standardTitle, usePageTitle } from '@/lib/page-title'
import { reposQueryOptions } from '@/lib/runs'

export const Route = createFileRoute('/app-urls')({
  component: AppURLsPage,
  loader: ({ context }) =>
    context.queryClient.ensureQueryData(reposQueryOptions),
})

function AppURLsPage() {
  usePageTitle(standardTitle('App URLs'))
  const { repo } = useActiveRepo()

  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-col gap-2">
        <Eyebrow glyph="action" className="text-primary">
          CONFIGURE
        </Eyebrow>
        <h1 className="text-balance text-2xl font-semibold tracking-tight text-foreground">
          App URLs
        </h1>
        <p className="text-pretty text-sm leading-relaxed text-muted-foreground">
          Where this repo&apos;s app runs locally, so the verify phase can drive
          it in a real browser. Entries stored here replace the ini APP_URL and
          APP_URLS keys for every run.
        </p>
      </header>

      <ProjectScopeGate action="edit app URLs">
        <AppURLsSection repo={repo ?? ''} />
      </ProjectScopeGate>
    </div>
  )
}
