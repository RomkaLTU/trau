import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import { Search } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import {
  Card,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { cn } from '@/lib/utils'
import { reposQueryOptions } from '@/lib/runs'
import { lessonsQueryOptions, type Lesson } from '@/lib/lessons'

export const Route = createFileRoute('/lessons')({
  component: Lessons,
  loader: ({ context }) => context.queryClient.ensureQueryData(reposQueryOptions),
})

function Lessons() {
  const { data, error, isPending } = useQuery(reposQueryOptions)
  const [selected, setSelected] = useState<string | null>(null)

  const repos = data?.repos ?? []
  const active =
    selected && repos.some((r) => r.name === selected)
      ? selected
      : repos.find((r) => r.live)?.name ?? repos[0]?.name ?? null

  return (
    <div className="flex flex-col gap-6">
      {error && <p className="text-sm text-destructive">{String(error)}</p>}
      {isPending && !error && (
        <p className="text-sm text-muted-foreground">Loading…</p>
      )}

      {data && repos.length === 0 && (
        <Card className="max-w-md">
          <CardHeader>
            <CardTitle>Lessons</CardTitle>
            <CardDescription>
              No repos yet. Lessons appear here once a trau loop records what it
              learned while repairing a run.
            </CardDescription>
          </CardHeader>
        </Card>
      )}

      {repos.length > 0 && (
        <div className="flex flex-wrap items-center gap-2">
          {repos.map((repo) => (
            <button
              key={repo.root}
              type="button"
              title={repo.root}
              onClick={() => setSelected(repo.name)}
              className={cn(
                'flex items-center gap-2 rounded-md border px-3 py-1.5 text-sm transition-colors',
                repo.name === active
                  ? 'border-transparent bg-accent text-accent-foreground'
                  : 'text-muted-foreground hover:text-foreground',
              )}
            >
              {repo.name}
              {repo.live && (
                <span className="size-1.5 rounded-full bg-emerald-500" />
              )}
            </button>
          ))}
        </div>
      )}

      {active && <LessonList repo={active} />}
    </div>
  )
}

function LessonList({ repo }: { repo: string }) {
  const { data, error, isPending } = useQuery(lessonsQueryOptions(repo))
  const [query, setQuery] = useState('')

  const lessons = data?.lessons ?? []
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return lessons
    return lessons.filter((l) => haystack(l).includes(q))
  }, [lessons, query])

  if (error) return <p className="text-sm text-destructive">{String(error)}</p>
  if (isPending) return <p className="text-sm text-muted-foreground">Loading…</p>
  if (lessons.length === 0) {
    return (
      <p className="text-sm text-muted-foreground">
        No lessons recorded for {repo} yet.
      </p>
    )
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center gap-3">
        <div className="relative w-full max-w-sm">
          <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
          <input
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search lessons…"
            className="h-9 w-full rounded-md border bg-transparent pl-9 pr-3 text-sm outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
          />
        </div>
        <span className="shrink-0 text-xs tabular-nums text-muted-foreground">
          {filtered.length} / {lessons.length}
        </span>
      </div>

      {filtered.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          No lessons match “{query}”.
        </p>
      ) : (
        <div className="flex flex-col gap-3">
          {filtered.map((lesson, i) => (
            <LessonCard key={`${lesson.recorded_at ?? ''}-${lesson.ticket ?? ''}-${i}`} lesson={lesson} />
          ))}
        </div>
      )}
    </div>
  )
}

const resultStyle: Record<string, string> = {
  repaired:
    'border-emerald-500/40 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400',
  quarantined: 'border-destructive/40 bg-destructive/10 text-destructive',
}

function LessonCard({ lesson }: { lesson: Lesson }) {
  return (
    <div className="rounded-lg border bg-card p-4 text-card-foreground shadow-xs">
      <div className="flex flex-wrap items-center gap-2">
        {lesson.ticket && (
          <span className="font-mono text-sm font-medium">{lesson.ticket}</span>
        )}
        {lesson.failure_type && (
          <Badge variant="outline">{lesson.failure_type}</Badge>
        )}
        {lesson.result && (
          <Badge variant="outline" className={resultStyle[lesson.result]}>
            {lesson.result}
          </Badge>
        )}
        {lesson.recorded_at && (
          <span className="ml-auto text-xs tabular-nums text-muted-foreground">
            {formatRecordedAt(lesson.recorded_at)}
          </span>
        )}
      </div>

      <p className="mt-2 text-sm">{lesson.lesson}</p>

      {lesson.evidence && lesson.evidence.length > 0 && (
        <ul className="mt-3 flex flex-col gap-1 border-l-2 border-border pl-3">
          {lesson.evidence.map((line, i) => (
            <li key={i} className="text-xs text-muted-foreground">
              {line}
            </li>
          ))}
        </ul>
      )}

      {lesson.tags && lesson.tags.length > 0 && (
        <div className="mt-3 flex flex-wrap gap-1.5">
          {lesson.tags.map((tag) => (
            <Badge key={tag} variant="secondary" className="font-normal">
              {tag}
            </Badge>
          ))}
        </div>
      )}
    </div>
  )
}

function haystack(l: Lesson): string {
  return [
    l.lesson,
    l.ticket,
    l.phase,
    l.failure_type,
    l.result,
    l.attempted_fix,
    ...(l.tags ?? []),
    ...(l.evidence ?? []),
  ]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
}

function formatRecordedAt(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}
