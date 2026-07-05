import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'

export interface Health {
  status: string
  version: string
  uptime_seconds: number
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
