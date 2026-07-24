import { type QueryClient } from '@tanstack/react-query'

import { apiFetch } from './api'
import { issueQueryOptions, type Issue } from './issues'

// ProviderPinSource is the slice of an issue the picker reads: its own pin and
// what it would inherit from its parent epic.
export type ProviderPinSource = Pick<
  Issue,
  'provider_pin' | 'provider_inherited' | 'provider_inherited_from'
>

export type ProviderPin =
  | { kind: 'pinned'; provider: string }
  | { kind: 'inherited'; provider: string; from: string }
  | { kind: 'default' }

// resolveProviderPin mirrors what a run resolves to below the one-shot override:
// the ticket's own pin, else the provider its parent epic pins, else the repo
// default.
export function resolveProviderPin(issue: ProviderPinSource): ProviderPin {
  if (issue.provider_pin) {
    return { kind: 'pinned', provider: issue.provider_pin }
  }
  if (issue.provider_inherited && issue.provider_inherited_from) {
    return {
      kind: 'inherited',
      provider: issue.provider_inherited,
      from: issue.provider_inherited_from,
    }
  }
  return { kind: 'default' }
}

// clearedProviderPin is what the ticket resolves to once its own pin is cleared —
// the inherited value when there is one, never straight back to the repo default.
export function clearedProviderPin(issue: ProviderPinSource): ProviderPin {
  return resolveProviderPin({ ...issue, provider_pin: '' })
}

export function providerPinLabel(pin: ProviderPin): string {
  switch (pin.kind) {
    case 'pinned':
      return pin.provider
    case 'inherited':
      return `Inherited from ${pin.from} (${pin.provider})`
    case 'default':
      return 'Repo default'
  }
}

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
