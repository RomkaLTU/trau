import { useQueries, useQuery } from '@tanstack/react-query'

import { reposQueryOptions, runsQueryOptions } from './runs'

export function useAttentionCount(): number {
  const { data } = useQuery(reposQueryOptions)
  const repos = data?.repos ?? []

  const results = useQueries({
    queries: repos.map((repo) => runsQueryOptions(repo.name)),
  })

  let count = 0
  for (const result of results) {
    for (const run of result.data?.runs ?? []) {
      if (run.failure_class) count++
    }
  }
  return count
}
