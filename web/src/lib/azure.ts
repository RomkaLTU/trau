import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'

// AzureFeature is one Feature an inbox create can nest the new work item under.
export interface AzureFeature {
  id: string
  title: string
}

// AzureCreateOptions is what an Azure DevOps create picks from: the
// requirement-level work-item types the project declares, its own default first,
// and the Features already on the repo's board. trau never files above
// requirement, so an Epic or a Feature is never among the types.
export interface AzureCreateOptions {
  types: string[]
  features: AzureFeature[]
}

async function fetchAzureCreateOptions(repo: string): Promise<AzureCreateOptions> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/azure/create-options`,
  )
  if (!res.ok) {
    const detail = (await res.json().catch(() => null)) as { error?: string } | null
    throw new Error(detail?.error ?? `create options request failed: ${res.status}`)
  }
  return res.json()
}

// The types come from the tracker and the Features from the hub's mirror, so the
// answer turns over only with a sync — held long enough to survive a review being
// remounted, refreshed on the next one.
export const azureCreateOptionsQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['azure-create-options', repo],
    queryFn: () => fetchAzureCreateOptions(repo),
    enabled: repo !== '',
    retry: false,
    staleTime: 60_000,
  })
