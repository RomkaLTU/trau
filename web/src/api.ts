export interface Health {
  status: string
  version: string
  uptime_seconds: number
}

export async function fetchHealth(): Promise<Health> {
  const res = await fetch('/api/v1/health')
  if (!res.ok) {
    throw new Error(`health request failed: ${res.status}`)
  }
  return res.json()
}
