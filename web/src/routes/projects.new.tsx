import { createFileRoute } from '@tanstack/react-router'

import { Eyebrow } from '@/components/trau/eyebrow'
import { OnboardingWizard } from '@/components/trau/onboarding/wizard'
import { standardTitle, usePageTitle } from '@/lib/page-title'

export const Route = createFileRoute('/projects/new')({
  component: NewProjectPage,
  validateSearch: (search: Record<string, unknown>): { path?: string } => {
    const path = search.path
    return typeof path === 'string' && path !== '' ? { path } : {}
  },
})

function NewProjectPage() {
  usePageTitle(standardTitle('Add a project'))
  const { path } = Route.useSearch()
  return (
    <div className="flex flex-col gap-8">
      <header className="flex flex-col gap-2">
        <Eyebrow glyph="action" className="text-primary">
          ONBOARDING
        </Eyebrow>
        <h1 className="text-balance text-2xl font-semibold tracking-tight text-foreground">
          Add a project
        </h1>
        <p className="text-pretty text-sm leading-relaxed text-muted-foreground">
          From a repo path to a live backlog — register, configure the tracker, and seed the
          issue store.
        </p>
      </header>

      <OnboardingWizard initialPath={path ?? ''} />
    </div>
  )
}
