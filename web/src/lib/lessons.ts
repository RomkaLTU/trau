import { queryOptions } from '@tanstack/react-query'

export interface Lesson {
  ticket?: string
  phase?: string
  failure_type?: string
  attempted_fix?: string
  evidence?: string[]
  result?: string
  lesson: string
  tags?: string[]
  recorded_at?: string
}

export interface LessonsResponse {
  repo: string
  lessons: Lesson[]
}

async function fetchLessons(repo: string): Promise<LessonsResponse> {
  const res = await fetch(`/api/v1/repos/${encodeURIComponent(repo)}/lessons`)
  if (!res.ok) {
    throw new Error(`lessons request failed: ${res.status}`)
  }
  return res.json()
}

export const lessonsQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['lessons', repo],
    queryFn: () => fetchLessons(repo),
    refetchInterval: 5000,
    enabled: repo !== '',
  })
