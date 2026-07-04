import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import {
  subscribeAllEvents,
  type FeedStatus,
  type RepoFeedEvent,
} from '@/lib/events'
import { fireStateNotification } from '@/lib/notifications'
import {
  STATE_CHANGE_KIND,
  deriveRecap,
  eventKey,
  toRecapItem,
  type Recap,
} from '@/lib/recap'

const LAST_SEEN_KEY = 'trau:lastSeen'

function loadLastSeen(): string | null {
  try {
    return localStorage.getItem(LAST_SEEN_KEY)
  } catch {
    return null
  }
}

function saveLastSeen(ts: string) {
  try {
    localStorage.setItem(LAST_SEEN_KEY, ts)
  } catch {
    // best-effort: a locked-down storage never blocks the recap
  }
}

export interface AwayMonitor {
  recap: Recap
  status: FeedStatus
  dismiss: () => void
}

// useAwayMonitor accumulates every repo's state_change events, tracks the client-
// side last-seen marker across visits, and derives the away recap from the two. It
// also fires a browser notification for each state change that lands live — after
// the tab opened — when shouldNotify is on, so backfilled history stays quiet.
export function useAwayMonitor(shouldNotify: boolean): AwayMonitor {
  const [events, setEvents] = useState<Map<string, RepoFeedEvent>>(
    () => new Map(),
  )
  const [status, setStatus] = useState<FeedStatus>('connecting')
  const [since, setSince] = useState<string | null>(() => {
    const stored = loadLastSeen()
    if (stored) return stored
    const now = new Date().toISOString()
    saveLastSeen(now)
    return now
  })

  const notified = useRef<Set<string>>(new Set())
  const floorMs = useRef<number>(Date.now())
  const shouldNotifyRef = useRef(shouldNotify)
  shouldNotifyRef.current = shouldNotify

  useEffect(() => {
    return subscribeAllEvents((ev) => {
      if (ev.kind !== STATE_CHANGE_KIND) return
      const key = eventKey(ev)
      setEvents((prev) => {
        if (prev.has(key)) return prev
        const next = new Map(prev)
        next.set(key, ev)
        return next
      })
      const ms = Date.parse(ev.ts)
      const live = Number.isNaN(ms) || ms > floorMs.current
      if (live && shouldNotifyRef.current && !notified.current.has(key)) {
        notified.current.add(key)
        const item = toRecapItem(ev)
        if (item) fireStateNotification(item)
      }
    }, setStatus)
  }, [])

  // Leaving the tab plants the marker at that moment; returning reads it back so
  // the recap spans exactly the away window. Staying put keeps the mount marker,
  // so the recap holds what changed since the previous visit.
  useEffect(() => {
    const onVisibility = () => {
      if (document.hidden) {
        saveLastSeen(new Date().toISOString())
      } else {
        const stored = loadLastSeen()
        if (stored) setSince(stored)
      }
    }
    document.addEventListener('visibilitychange', onVisibility)
    return () => document.removeEventListener('visibilitychange', onVisibility)
  }, [])

  const dismiss = useCallback(() => {
    const now = new Date().toISOString()
    saveLastSeen(now)
    setSince(now)
  }, [])

  const recap = useMemo(
    () => deriveRecap([...events.values()], since),
    [events, since],
  )

  return { recap, status, dismiss }
}
