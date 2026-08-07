import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'

export interface Health {
  status: string
  version: string
  webBuild?: string
  uptime_seconds: number
  token_required: boolean
}

// A hub older than the stamp handshake answers no webBuild at all: unknown, not
// a mismatch.
export function servedBuild(health: Health | undefined): string {
  return health?.webBuild ?? ''
}

async function fetchHealth(): Promise<Health> {
  const res = await apiFetch('/api/v1/health')
  if (!res.ok) {
    throw new Error(`health request failed: ${res.status}`)
  }
  return res.json()
}

export const healthQueryOptions = queryOptions({
  queryKey: ['health'],
  queryFn: fetchHealth,
  refetchInterval: 5000,
})
