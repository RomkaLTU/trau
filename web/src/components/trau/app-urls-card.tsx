import { Link } from '@tanstack/react-router'
import { ArrowRight, TriangleAlert } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { useActiveRepo } from '@/components/trau/active-repo'
import { URLLink, WorkspaceBadge } from '@/components/trau/app-urls-panel'
import { Eyebrow } from '@/components/trau/eyebrow'
import { Panel } from '@/components/trau/overview'
import { useAppURLTargets } from '@/lib/app-urls'

const ROW = 'flex flex-wrap items-center gap-2.5 px-5 py-2.5'

export function AppURLsCard() {
  const { repo, isAll } = useActiveRepo()
  if (isAll || !repo) return null

  return (
    <div className="flex flex-col gap-2">
      <Eyebrow glyph="action">APP URLS</Eyebrow>
      <CardBody repo={repo} />
    </div>
  )
}

function CardBody({ repo }: { repo: string }) {
  const { entries, fallback, isPending, error } = useAppURLTargets(repo)
  const rows = entries.length > 0 ? entries : fallback

  return (
    <Panel title="app urls" count={rows.length}>
      <div className="flex flex-col">
        {error ? (
          <p
            className="inline-flex items-center gap-2 px-5 py-4 font-mono text-xs text-fail"
            role="alert"
          >
            <TriangleAlert className="size-3.5" aria-hidden="true" />
            {error.message}
          </p>
        ) : isPending ? (
          <p className="px-5 py-4 font-mono text-xs text-muted-foreground">
            loading app urls…
          </p>
        ) : rows.length === 0 ? (
          <p className="px-5 py-4 font-sans text-sm leading-relaxed text-muted-foreground">
            No app URL for this repo, so browser verify stays advisory — it never
            drives the UI it just changed.
          </p>
        ) : entries.length > 0 ? (
          <ul className="flex flex-col divide-y divide-border/60">
            {entries.map((entry) => (
              <li key={entry.id} className={ROW}>
                <WorkspaceBadge workspace={entry.workspace} />
                {entry.label !== '' && (
                  <span className="min-w-0 truncate font-mono text-xs text-foreground">
                    {entry.label}
                  </span>
                )}
                <span className="ml-auto min-w-0">
                  <URLLink url={entry.url} />
                </span>
              </li>
            ))}
          </ul>
        ) : (
          <>
            <p className="px-5 pt-3 text-xs leading-relaxed text-muted-foreground">
              Falling back to the ini{' '}
              <span className="font-mono text-foreground">APP_URL</span> and{' '}
              <span className="font-mono text-foreground">APP_URLS</span> keys —
              the first entry added here replaces both.
            </p>
            <ul className="flex flex-col divide-y divide-border/60">
              {fallback.map((f) => (
                <li key={f.workspace} className={ROW}>
                  <WorkspaceBadge workspace={f.workspace} />
                  <Badge
                    variant="outline"
                    className="shrink-0 font-mono text-[0.7rem] font-normal text-muted-foreground"
                  >
                    from config
                  </Badge>
                  <span className="ml-auto min-w-0">
                    <URLLink url={f.url} />
                  </span>
                </li>
              ))}
            </ul>
          </>
        )}

        <Link
          to="/app-urls"
          className="flex items-center gap-2 border-t border-border/60 px-5 py-2 font-mono text-xs text-faint transition-colors hover:bg-secondary/40 hover:text-muted-foreground"
        >
          {rows.length === 0 ? 'add an app url' : 'manage app urls'}
          <ArrowRight className="size-3.5" aria-hidden="true" />
        </Link>
      </div>
    </Panel>
  )
}
