import { type QueryClient } from '@tanstack/react-query'

import { apiFetch } from './api'
import { issueQueryOptions, type Issue } from './issues'

async function errorMessage(res: Response, fallback: string): Promise<string> {
  const detail = (await res.json().catch(() => null)) as { error?: string } | null
  return detail?.error ?? `${fallback}: ${res.status}`
}

// pinProvider persists the provider every run of the ticket uses and answers with
// the updated issue. An empty provider clears the pin back to the repo default.
export async function pinProvider(
  repo: string,
  id: string,
  provider: string,
): Promise<Issue> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/issues/${encodeURIComponent(id)}/provider`,
    {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ provider }),
    },
  )
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'pin provider failed'))
  }
  return res.json()
}

// publishProviderPin writes the pinned issue in place, then refreshes the board
// rows and queue items that carry the same tag.
export function publishProviderPin(
  client: QueryClient,
  repo: string,
  issue: Issue,
): void {
  client.setQueryData(issueQueryOptions(repo, issue.id).queryKey, issue)
  void client.invalidateQueries({ queryKey: ['backlog', repo] })
  void client.invalidateQueries({ queryKey: ['queue', repo] })
}
