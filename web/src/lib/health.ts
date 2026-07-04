import { queryOptions } from '@tanstack/react-query'

export interface Health {
  status: string
  version: string
  uptime_seconds: number
}

async function fetchHealth(): Promise<Health> {
  const res = await fetch('/api/v1/health')
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
