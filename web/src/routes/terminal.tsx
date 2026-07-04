import { useEffect, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'

import {
  Card,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Terminal } from '@/components/terminal'
import { instancesQueryOptions, type RepoView } from '@/lib/instances'
import {
  transcriptsQueryOptions,
  type TranscriptView,
} from '@/lib/transcripts'

export const Route = createFileRoute('/terminal')({
  component: TerminalPage,
  loader: ({ context }) =>
    context.queryClient.ensureQueryData(instancesQueryOptions),
})

const FOLLOW_NEWEST = ''

function TerminalPage() {
  const { data, error, isPending } = useQuery(instancesQueryOptions)
  const repos = useMemo(() => sortRepos(data?.repos ?? []), [data])

  const [repo, setRepo] = useState('')
  const [id, setID] = useState(FOLLOW_NEWEST)

  useEffect(() => {
    if (repos.length === 0) return
    if (!repos.some((r) => r.name === repo)) {
      setRepo(repos[0].name)
      setID(FOLLOW_NEWEST)
    }
  }, [repos, repo])

  return (
    <div className="flex flex-col gap-6">
      {error && <p className="text-sm text-destructive">{String(error)}</p>}
      {isPending && !error && (
        <p className="text-sm text-muted-foreground">Loading…</p>
      )}

      {data && repos.length === 0 && (
        <Card className="max-w-md">
          <CardHeader>
            <CardTitle>Terminal</CardTitle>
            <CardDescription>
              No repos have run a trau loop on this machine yet.
            </CardDescription>
          </CardHeader>
        </Card>
      )}

      {repo && (
        <>
          <div className="flex flex-wrap items-end gap-4">
            <Field label="Repo">
              <select
                className={selectClass}
                value={repo}
                onChange={(e) => {
                  setRepo(e.target.value)
                  setID(FOLLOW_NEWEST)
                }}
              >
                {repos.map((r) => (
                  <option key={r.name} value={r.name}>
                    {r.name}
                    {r.live ? ' • live' : ''}
                  </option>
                ))}
              </select>
            </Field>
            <PhaseField repo={repo} value={id} onChange={setID} />
          </div>
          <Terminal
            key={`${repo}:${id || 'newest'}`}
            repo={repo}
            id={id || undefined}
          />
        </>
      )}
    </div>
  )
}

function PhaseField({
  repo,
  value,
  onChange,
}: {
  repo: string
  value: string
  onChange: (id: string) => void
}) {
  const { data } = useQuery(transcriptsQueryOptions(repo))
  const transcripts = data?.transcripts ?? []

  return (
    <Field label="Phase">
      <select
        className={selectClass}
        value={value}
        onChange={(e) => onChange(e.target.value)}
      >
        <option value={FOLLOW_NEWEST}>Live — newest</option>
        {transcripts.map((t) => (
          <option key={t.id} value={t.id}>
            {phaseOption(t)}
          </option>
        ))}
      </select>
    </Field>
  )
}

function phaseOption(t: TranscriptView): string {
  const when = new Date(t.modified)
  const time = Number.isNaN(when.getTime())
    ? ''
    : ` · ${when.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}`
  return `${t.label}${t.live ? ' (newest)' : ''}${time}`
}

function Field({
  label,
  children,
}: {
  label: string
  children: React.ReactNode
}) {
  return (
    <label className="flex flex-col gap-1.5 text-sm">
      <span className="text-xs font-medium text-muted-foreground">{label}</span>
      {children}
    </label>
  )
}

const selectClass =
  'h-9 rounded-md border bg-background px-3 text-sm shadow-sm focus:outline-none focus:ring-2 focus:ring-ring'

// sortRepos floats live repos to the top so the default selection is one that is
// actively producing a transcript.
function sortRepos(repos: RepoView[]): RepoView[] {
  return [...repos].sort((a, b) => {
    if (a.live !== b.live) return a.live ? -1 : 1
    return a.name.localeCompare(b.name)
  })
}
