import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ArrowRight, GitBranch, TriangleAlert } from 'lucide-react'

import { useActiveRepo } from '@/components/trau/active-repo'
import { Eyebrow } from '@/components/trau/eyebrow'
import { Panel } from '@/components/trau/overview'
import { AppStateBadge } from '@/components/trau/worktree-app'
import { activeWorktrees, worktreesQueryOptions } from '@/lib/worktrees'

const ROW = 'flex flex-wrap items-center gap-2.5 px-5 py-2.5'

/**
 * The trees this repo has standing, and the way into the panel that operates them.
 * A repo that has never used worktrees has nothing to say here, so the card stays
 * out of the overview entirely.
 */
export function WorktreesCard() {
  const { repo, isAll } = useActiveRepo()
  if (isAll || !repo) return null
  return <CardBody repo={repo} />
}

function CardBody({ repo }: { repo: string }) {
  const { data, error, isPending } = useQuery(worktreesQueryOptions(repo))
  const rows = activeWorktrees(data ?? [])

  if (!error && (isPending || (data ?? []).length === 0)) return null

  return (
    <div className="flex flex-col gap-2">
      <Eyebrow glyph="action">WORKTREES</Eyebrow>
      <Panel title="worktrees" count={rows.length}>
        <div className="flex flex-col">
          {error ? (
            <p
              className="inline-flex items-center gap-2 px-5 py-4 font-mono text-xs text-fail"
              role="alert"
            >
              <TriangleAlert className="size-3.5" aria-hidden="true" />
              {error.message}
            </p>
          ) : rows.length === 0 ? (
            <p className="px-5 py-4 font-sans text-sm leading-relaxed text-muted-foreground">
              No tree is standing right now — every ticket that had one gave it
              up when it settled.
            </p>
          ) : (
            <ul className="flex flex-col divide-y divide-border/60">
              {rows.map((row) => (
                <li key={row.id} className={ROW}>
                  <Link
                    to="/runs/$repo/$ticket"
                    params={{ repo, ticket: row.ticket }}
                    className="font-mono text-xs text-foreground underline-offset-4 hover:underline"
                  >
                    {row.ticket}
                  </Link>
                  {row.branch && (
                    <span className="inline-flex min-w-0 items-center gap-1.5 font-mono text-xs text-muted-foreground">
                      <GitBranch className="size-3.5 shrink-0" aria-hidden="true" />
                      <span className="truncate">{row.branch}</span>
                    </span>
                  )}
                  <span className="ml-auto inline-flex items-center gap-2.5">
                    {row.port ? (
                      <span className="font-mono text-xs tabular-nums text-muted-foreground">
                        :{row.port}
                      </span>
                    ) : null}
                    <AppStateBadge worktree={row} />
                  </span>
                </li>
              ))}
            </ul>
          )}

          <Link
            to="/worktrees"
            className="flex items-center gap-2 border-t border-border/60 px-5 py-2 font-mono text-xs text-faint transition-colors hover:bg-secondary/40 hover:text-muted-foreground"
          >
            manage worktrees
            <ArrowRight className="size-3.5" aria-hidden="true" />
          </Link>
        </div>
      </Panel>
    </div>
  )
}
