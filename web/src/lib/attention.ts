import { useQuery } from '@tanstack/react-query'

import { runsQueryOptions, type Run } from './runs'

export interface AttentionRun extends Run {
  repo: string
}

export function useAttentionRuns(repo: string | null): AttentionRun[] {
  const { data } = useQuery(runsQueryOptions(repo ?? ''))

  const attention: AttentionRun[] = []
  for (const run of data?.runs ?? []) {
    if (run.failure_class) attention.push({ ...run, repo: repo ?? '' })
  }
  return attention
}

export function useAttentionCount(repo: string | null): number {
  return useAttentionRuns(repo).length
}
