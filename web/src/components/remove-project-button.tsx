import { Trash2 } from 'lucide-react'

import { RepoRowAction } from '@/components/repo-row-action'
import type { RepoView } from '@/lib/instances'
import {
  removalPlan,
  removalSummary,
  removeProjectRepos,
  type ProjectRemoval,
  type ProjectView,
} from '@/lib/projects'

// RemoveProjectButton clears a whole folder off the hub: the project that grouped
// the repos when the wizard registered them, gone in one confirm instead of one per
// member. Members the hub would refuse stay behind, so the dialog names both sides
// before the click and the toast names the ones that actually went. The trigger only
// goes dead when every member is blocked — while one is removable there is still
// something to do.
export function RemoveProjectButton({
  project,
  repos,
}: {
  project: ProjectView
  repos: RepoView[]
}) {
  const { removing, blocked } = removalPlan(repos)
  // Every member's own reason, since clearing the first one need not be the whole of
  // what is holding the folder.
  const reasons = blocked
    .map((member) => `${member.name} — ${member.reason}`)
    .join('\n')

  return (
    <RepoRowAction<ProjectRemoval>
      icon={Trash2}
      label="Remove all"
      pendingLabel="Removing…"
      blocked={removing.length === 0 ? reasons : null}
      destructive
      windowTitle="remove project"
      title={`Remove ${project.name} from the hub?`}
      description={
        <>
          A removed repo leaves this list along with its cached attachments and
          tracker sync state. Nothing on disk is deleted, and running trau in it
          again brings it back.
          <span className="mt-2 flex flex-col gap-0.5">
            <span>{removing.length} will go:</span>
            {removing.map((name) => (
              <span key={name} className="font-mono text-xs">
                {name}
              </span>
            ))}
          </span>
          {blocked.length > 0 && (
            <span className="mt-2 flex flex-col gap-0.5">
              <span>{blocked.length} will stay:</span>
              {blocked.map((member) => (
                <span key={member.name}>
                  <span className="font-mono text-xs">{member.name}</span> —{' '}
                  {member.reason}
                </span>
              ))}
            </span>
          )}
        </>
      }
      confirmLabel="Remove all"
      successMessage={removalSummary}
      action={() => removeProjectRepos(project.id)}
    />
  )
}
