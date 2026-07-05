import { useQueries, useQuery } from '@tanstack/react-query'

import { reposQueryOptions, runsQueryOptions, type Run } from './runs'

export interface AttentionRun extends Run {
  repo: string
}

export function useAttentionRuns(): AttentionRun[] {
  const { data } = useQuery(reposQueryOptions)
  const repos = data?.repos ?? []

  const results = useQueries({
    queries: repos.map((repo) => runsQueryOptions(repo.name)),
  })

  const attention: AttentionRun[] = []
  results.forEach((result, i) => {
    const repo = repos[i]?.name ?? ''
    for (const run of result.data?.runs ?? []) {
      if (run.failure_class) attention.push({ ...run, repo })
    }
  })
  return attention
}

export function useAttentionCount(): number {
  return useAttentionRuns().length
}
