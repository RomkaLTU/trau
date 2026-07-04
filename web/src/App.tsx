import { useEffect, useState } from 'react'
import { fetchHealth, type Health } from './api'

function formatUptime(seconds: number): string {
  const s = Math.floor(seconds)
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const rem = s % 60
  if (h > 0) return `${h}h ${m}m ${rem}s`
  if (m > 0) return `${m}m ${rem}s`
  return `${rem}s`
}

export function App() {
  const [health, setHealth] = useState<Health | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let active = true
    const load = () => {
      fetchHealth()
        .then((h) => {
          if (!active) return
          setHealth(h)
          setError(null)
        })
        .catch((e) => {
          if (active) setError(String(e))
        })
    }
    load()
    const id = setInterval(load, 5000)
    return () => {
      active = false
      clearInterval(id)
    }
  }, [])

  return (
    <main className="app">
      <h1>trau</h1>
      <p className="tagline">autonomous, ticket-driven development loop</p>
      {error && <p className="error">{error}</p>}
      {health && (
        <dl className="health">
          <div>
            <dt>status</dt>
            <dd className={health.status === 'ok' ? 'ok' : 'bad'}>{health.status}</dd>
          </div>
          <div>
            <dt>version</dt>
            <dd>{health.version}</dd>
          </div>
          <div>
            <dt>uptime</dt>
            <dd>{formatUptime(health.uptime_seconds)}</dd>
          </div>
        </dl>
      )}
      {!health && !error && <p className="muted">loading…</p>}
    </main>
  )
}
