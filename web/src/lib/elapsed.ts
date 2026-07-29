import { useEffect, useState } from 'react'

// useNow re-renders its caller on a fixed interval, so a relative time on screen
// keeps ticking without every surface owning a timer of its own.
export function useNow(intervalMs: number): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs)
    return () => clearInterval(id)
  }, [intervalMs])
  return now
}

// syncedAgo renders how long ago a repo's last sync landed, so the loop card and
// the backlog's sync control read the same way. The first few seconds are "just
// now", keeping a line that just settled out of a pull from ticking single digits.
export function syncedAgo(fromISO: string, now: number): string {
  const s = Math.max(0, Math.floor((now - new Date(fromISO).getTime()) / 1000))
  if (s < 10) return 'just now'
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (h > 0) return `${h}h ${m}m ago`
  if (m > 0) return `${m}m ${s % 60}s ago`
  return `${s}s ago`
}
