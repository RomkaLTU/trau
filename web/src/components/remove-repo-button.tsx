import { Trash2 } from 'lucide-react'

import { RepoRowAction } from '@/components/repo-row-action'
import { forgetRepo } from '@/lib/instances'

// RemoveRepoButton takes a repo off the hub's list for good. It is the only
// control that reaches a repo the hub merely watched a loop run in — a scratch
// clone, a worktree that is gone — which no unregister can clear because it was
// never registered. blocked is the reason the hub would refuse it, if it has one.
export function RemoveRepoButton({
  repo,
  root,
  blocked,
}: {
  repo: string
  root: string
  blocked: string | null
}) {
  return (
    <RepoRowAction
      icon={Trash2}
      label="Remove"
      pendingLabel="Removing…"
      blocked={blocked}
      destructive
      windowTitle="remove repo"
      title={`Remove ${repo} from the hub?`}
      description={
        <>
          <span className="font-mono text-xs">{root}</span> leaves this list
          along with its cached attachments and tracker sync state. Nothing on
          disk is deleted, and running trau in it again brings it back.
        </>
      }
      confirmLabel="Remove"
      successMessage={`${repo} removed from the hub`}
      action={() => forgetRepo(root)}
    />
  )
}
