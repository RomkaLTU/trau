import { FolderMinus } from 'lucide-react'

import { RepoRowAction } from '@/components/repo-row-action'
import { unregisterRepo } from '@/lib/instances'

// UnregisterRepoButton is the exact inverse of Make startable, so it says so:
// the repo keeps its runs and its row, the hub just stops being allowed to start
// loops in it. Removing the row is RemoveRepoButton's job.
export function UnregisterRepoButton({
  repo,
  root,
}: {
  repo: string
  root: string
}) {
  return (
    <RepoRowAction
      icon={FolderMinus}
      label="Make observe-only"
      pendingLabel="Making observe-only…"
      windowTitle="make observe-only"
      title={`Make ${repo} observe-only?`}
      description="The hub stops starting loops in it. Its runs stay browsable, it keeps its place in this list, and nothing on disk is deleted."
      confirmLabel="Make observe-only"
      successMessage={`${repo} is observe-only again`}
      action={() => unregisterRepo(root)}
    />
  )
}
