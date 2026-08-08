import { createFileRoute } from '@tanstack/react-router'

import { Eyebrow, ProjectScopeGate, useActiveRepo } from '@/components/trau'
import { WorktreesSection } from '@/components/trau/worktrees-panel'
import { standardTitle, usePageTitle } from '@/lib/page-title'
import { reposQueryOptions } from '@/lib/runs'

export const Route = createFileRoute('/worktrees')({
  component: WorktreesPage,
  loader: ({ context }) =>
    context.queryClient.ensureQueryData(reposQueryOptions),
})

function WorktreesPage() {
  usePageTitle(standardTitle('Worktrees'))
  const { repo } = useActiveRepo()

  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-col gap-2">
        <Eyebrow glyph="action" className="text-primary">
          CONFIGURE
        </Eyebrow>
        <h1 className="text-balance text-2xl font-semibold tracking-tight text-foreground">
          Worktrees
        </h1>
        <p className="text-pretty text-sm leading-relaxed text-muted-foreground">
          The per-ticket git worktrees this repo has on disk, the app each one
          serves, and the controls to start, bounce or stop it. Removing a tree
          is refused while a run still holds the repo.
        </p>
      </header>

      <ProjectScopeGate action="operate worktrees">
        <WorktreesSection repo={repo ?? ''} />
      </ProjectScopeGate>
    </div>
  )
}
