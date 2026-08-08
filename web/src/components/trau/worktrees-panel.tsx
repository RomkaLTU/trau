import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { GitBranch, Loader2, Trash2, TriangleAlert } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { TerminalCard } from '@/components/trau/terminal-card'
import {
  AppStateBadge,
  WorktreeAppControls,
} from '@/components/trau/worktree-app'
import type { Worktree } from '@/lib/rundetail'
import {
  activeWorktrees,
  removeWorktree,
  worktreeAge,
  worktreesQueryOptions,
} from '@/lib/worktrees'

export function WorktreesSection({ repo }: { repo: string }) {
  const { data, error, isPending, refetch } = useQuery(
    worktreesQueryOptions(repo),
  )
  const rows = activeWorktrees(data ?? [])

  return (
    <TerminalCard title="worktrees" bodyClassName="p-0">
      <div className="flex flex-col">
        <p className="border-b border-border/60 px-4 py-2 text-xs leading-relaxed text-muted-foreground">
          Every tree this repo has standing on disk, with the app it serves. A
          tree is removed for you when its ticket settles; Remove is for the
          straggler whose ticket settled some other way.
        </p>

        {error ? (
          <div className="flex flex-col items-start gap-2 px-4 py-3">
            <p
              className="inline-flex items-center gap-2 font-mono text-xs text-fail"
              role="alert"
            >
              <TriangleAlert className="size-3.5" aria-hidden="true" />
              {error.message}
            </p>
            <Button
              variant="outline"
              size="sm"
              className="font-mono text-xs"
              onClick={() => void refetch()}
            >
              retry
            </Button>
          </div>
        ) : isPending ? (
          <p className="px-4 py-3 font-mono text-xs text-muted-foreground">
            loading worktrees…
          </p>
        ) : rows.length === 0 ? (
          <p className="px-4 py-4 font-sans text-sm leading-relaxed text-muted-foreground">
            No worktree is standing in this repo. With{' '}
            <span className="font-mono text-foreground">WORKTREES=1</span> each
            run provisions its own tree and gives it up when the ticket settles.
          </p>
        ) : (
          <ul className="flex flex-col divide-y divide-border/60">
            {rows.map((row) => (
              <WorktreeRow key={row.id} repo={repo} worktree={row} />
            ))}
          </ul>
        )}
      </div>
    </TerminalCard>
  )
}

function WorktreeRow({ repo, worktree }: { repo: string; worktree: Worktree }) {
  const queryClient = useQueryClient()
  const mutation = useMutation({
    mutationFn: () => removeWorktree(repo, worktree.id),
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: ['worktrees', repo] }),
  })

  return (
    <li className="flex flex-col gap-2.5 px-4 py-3">
      <div className="flex flex-wrap items-center gap-2.5">
        <Link
          to="/runs/$repo/$ticket"
          params={{ repo, ticket: worktree.ticket }}
          className="font-mono text-sm text-foreground underline-offset-4 hover:underline"
        >
          {worktree.ticket}
        </Link>
        {worktree.branch && (
          <span className="inline-flex min-w-0 items-center gap-1.5 font-mono text-xs text-muted-foreground">
            <GitBranch className="size-3.5 shrink-0" aria-hidden="true" />
            <span className="truncate">{worktree.branch}</span>
          </span>
        )}
        {worktree.port ? (
          <span className="font-mono text-xs tabular-nums text-muted-foreground">
            :{worktree.port}
          </span>
        ) : null}
        <AppStateBadge worktree={worktree} />
        {worktree.created_at && (
          <span
            className="font-mono text-[0.7rem] text-faint"
            title={worktree.created_at}
          >
            {worktreeAge(worktree.created_at)}
          </span>
        )}
        <Button
          size="sm"
          variant="outline"
          className="ml-auto font-mono"
          disabled={mutation.isPending}
          onClick={() => mutation.mutate()}
        >
          {mutation.isPending ? (
            <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
          ) : (
            <Trash2 className="size-3.5" aria-hidden="true" />
          )}
          remove
        </Button>
      </div>
      <p className="break-all font-mono text-[0.7rem] text-faint">
        {worktree.path}
      </p>
      <WorktreeAppControls repo={repo} worktree={worktree} />
      {mutation.isError && (
        <p
          className="inline-flex items-center gap-2 font-mono text-[0.7rem] text-fail"
          role="alert"
        >
          <TriangleAlert className="size-3.5" aria-hidden="true" />
          {mutation.error instanceof Error
            ? mutation.error.message
            : 'remove failed'}
        </p>
      )}
    </li>
  )
}
