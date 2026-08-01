import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { TriangleAlert } from 'lucide-react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { useActiveRepo } from '@/components/trau/active-repo'
import { TerminalCard } from '@/components/trau/terminal-card'
import {
  FieldLabel,
  Hint,
  TextInput,
  Toggle,
} from '@/components/trau/onboarding/ui'
import type { RepoView } from '@/lib/instances'
import {
  PROJECT_DEFAULT_KEYS,
  matchesProjectDefaults,
  projectDefaultKeys,
  projectDefaults,
  projectDefaultsModified,
  projectMembers,
  projectsQueryOptions,
  projectTrackerQueryOptions,
  writeProjectTracker,
  type ProjectDefaults,
} from '@/lib/projects'

const SECTION_ID = 'project-defaults'

function useProjectMembership(repo: string) {
  const { repos } = useActiveRepo()
  const projects = useQuery(projectsQueryOptions).data?.projects ?? []
  return projectMembers(repo, repos, projects)
}

export function ProjectDefaultsSection({
  repo,
  query = '',
}: {
  repo: string
  query?: string
}) {
  const { project, members } = useProjectMembership(repo)

  if (project === '' || !matchesProjectDefaults(query)) return null

  return <DefaultsEditor key={project} project={project} members={members} />
}

// useProjectDefaultsNav is the section's own answer to whether it renders at
// all, so the settings nav and the search empty state cannot disagree with the
// panel and leave a link pointing at an anchor that is not there.
export function useProjectDefaultsNav(repo: string) {
  const { project } = useProjectMembership(repo)
  const { data } = useQuery(projectTrackerQueryOptions(project))

  return useMemo(
    () =>
      project === ''
        ? null
        : {
            id: SECTION_ID,
            title: 'Project defaults',
            count: PROJECT_DEFAULT_KEYS.length,
            modified: projectDefaultsModified(data),
          },
    [project, data],
  )
}

function DefaultsEditor({
  project,
  members,
}: {
  project: string
  members: RepoView[]
}) {
  const queryClient = useQueryClient()
  const { data, error, isPending } = useQuery(
    projectTrackerQueryOptions(project),
  )
  const [draft, setDraft] = useState<Partial<ProjectDefaults> | null>(null)
  const fields = { ...projectDefaults(data), ...draft }

  const save = useMutation({
    mutationFn: () =>
      writeProjectTracker(project, projectDefaultKeys(draft ?? {})),
    onSuccess: (tracker) => {
      queryClient.setQueryData(
        projectTrackerQueryOptions(project).queryKey,
        tracker,
      )
      void queryClient.invalidateQueries({ queryKey: ['config'] })
      setDraft(null)
      toast('Project defaults saved to every member repo')
    },
    onError: (err: Error) => toast.error(err.message),
  })

  return (
    <section id="project-defaults" className="scroll-mt-6">
      <TerminalCard title="Project defaults" bodyClassName="p-0">
        <div className="flex flex-col">
          <p className="border-b border-border/60 px-4 py-2 text-xs leading-relaxed text-muted-foreground">
            Answers the whole project gives. Saving writes them into every member
            repo's .trau.ini — a repo that sets one for itself keeps its own
            value.
          </p>
          {error && (
            <p
              className="inline-flex items-center gap-2 px-4 py-3 font-mono text-xs text-fail"
              role="alert"
            >
              <TriangleAlert className="size-3.5" aria-hidden="true" />
              {error.message}
            </p>
          )}
          {isPending && (
            <p className="px-4 py-3 font-mono text-xs text-muted-foreground">
              Loading…
            </p>
          )}
          {data && (
            <div className="flex flex-col gap-5 p-4">
              <div className="flex flex-col gap-2">
                <FieldLabel htmlFor="project-ready-label">
                  ready label
                </FieldLabel>
                <TextInput
                  id="project-ready-label"
                  className="max-w-sm"
                  value={fields.readyLabel}
                  placeholder="ready-for-agent"
                  onChange={(e) =>
                    setDraft({ ...draft, readyLabel: e.target.value })
                  }
                />
                <Hint>
                  Only tickets carrying this label are picked up by the loop.
                </Hint>
              </div>

              <Toggle
                id="project-epic-flow"
                checked={fields.epicFlow}
                onChange={(epicFlow) => setDraft({ ...draft, epicFlow })}
                label="epic flow"
                description="Stack an epic's sub-issues on a shared integration branch instead of one PR each."
              />
            </div>
          )}
          <div className="flex items-center gap-3 border-t border-border/60 px-4 py-3">
            <Button
              size="sm"
              disabled={draft === null || save.isPending}
              onClick={() => save.mutate()}
            >
              {save.isPending ? 'Saving…' : 'Save'}
            </Button>
            <span className="min-w-0 truncate font-mono text-xs text-faint">
              seeded into {members.map((m) => m.name).join(', ')}
            </span>
          </div>
        </div>
      </TerminalCard>
    </section>
  )
}
