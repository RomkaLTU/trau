import { useEffect, useMemo, useState } from 'react'
import { queryOptions, useQuery } from '@tanstack/react-query'

export interface FeedEvent {
  id: string
  ts: string
  kind: string
  phase?: string
  msg?: string
  fields?: Record<string, unknown>
}

export interface RepoFeedEvent extends FeedEvent {
  repo: string
}

export interface EventsResponse {
  repo: string
  events: FeedEvent[]
}

const FEED_CAP = 500

async function fetchRecentEvents(repo: string): Promise<EventsResponse> {
  const res = await fetch(`/api/v1/repos/${encodeURIComponent(repo)}/events`)
  if (!res.ok) {
    throw new Error(`events request failed: ${res.status}`)
  }
  return res.json()
}

export const recentEventsQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['events', repo],
    queryFn: () => fetchRecentEvents(repo),
    enabled: repo !== '',
    staleTime: Infinity,
  })

export type FeedStatus = 'connecting' | 'live' | 'error'

export interface EventFeed {
  events: FeedEvent[]
  status: FeedStatus
  error: unknown
}

export function useEventFeed(repo: string): EventFeed {
  const recent = useQuery(recentEventsQueryOptions(repo))
  const [byId, setById] = useState<Map<string, FeedEvent>>(() => new Map())
  const [status, setStatus] = useState<FeedStatus>('connecting')

  useEffect(() => {
    setById(new Map())
    setStatus('connecting')
  }, [repo])

  useEffect(() => {
    if (!recent.data) return
    setById((prev) => mergeEvents(prev, recent.data.events))
  }, [recent.data])

  useEffect(() => {
    if (!repo) return
    return subscribeFeed(
      repo,
      (ev) => setById((prev) => mergeEvents(prev, [ev])),
      setStatus,
    )
  }, [repo])

  const events = useMemo(
    () =>
      [...byId.values()]
        .sort((a, b) => Number(b.id) - Number(a.id))
        .slice(0, FEED_CAP),
    [byId],
  )

  return { events, status, error: recent.error }
}

type EventListener = (ev: FeedEvent) => void
type AllListener = (ev: RepoFeedEvent) => void
type StatusListener = (status: FeedStatus) => void

const feedListeners = new Map<string, Set<EventListener>>()
const allListeners = new Set<AllListener>()
const feedStatusListeners = new Set<StatusListener>()
let feedSource: EventSource | null = null
let feedStatus: FeedStatus = 'connecting'
let feedRefs = 0

// subscribeFeed hangs every repo's feed off a single machine-wide EventSource, so
// a monitor watching more repos than the browser's per-origin connection cap does
// not leave later feeds stuck connecting. Frames are routed by their repo tag.
function subscribeFeed(
  repo: string,
  onEvent: EventListener,
  onStatus: StatusListener,
): () => void {
  let listeners = feedListeners.get(repo)
  if (!listeners) {
    listeners = new Set()
    feedListeners.set(repo, listeners)
  }
  listeners.add(onEvent)
  feedStatusListeners.add(onStatus)
  feedRefs++
  openFeedSource()
  onStatus(feedStatus)

  return () => {
    listeners.delete(onEvent)
    if (listeners.size === 0) feedListeners.delete(repo)
    feedStatusListeners.delete(onStatus)
    releaseFeed()
  }
}

// subscribeAllEvents taps the same machine-wide EventSource but receives every
// repo's frames, each carrying its repo tag. It backs the away recap and browser
// notifications, which reason across all repos rather than one.
export function subscribeAllEvents(
  onEvent: AllListener,
  onStatus: StatusListener,
): () => void {
  allListeners.add(onEvent)
  feedStatusListeners.add(onStatus)
  feedRefs++
  openFeedSource()
  onStatus(feedStatus)

  return () => {
    allListeners.delete(onEvent)
    feedStatusListeners.delete(onStatus)
    releaseFeed()
  }
}

function releaseFeed() {
  feedRefs--
  if (feedRefs === 0 && feedSource) {
    feedSource.close()
    feedSource = null
    feedStatus = 'connecting'
  }
}

function openFeedSource() {
  if (feedSource) return
  setFeedStatus('connecting')
  const source = new EventSource('/api/v1/events/stream')
  source.onopen = () => setFeedStatus('live')
  source.onerror = () => setFeedStatus('error')
  source.onmessage = (e) => {
    let msg: FeedEvent & { repo?: string }
    try {
      msg = JSON.parse(e.data)
    } catch {
      return
    }
    if (!msg.repo) return
    const tagged = msg as RepoFeedEvent
    for (const notify of allListeners) notify(tagged)
    const listeners = feedListeners.get(msg.repo)
    if (listeners) for (const notify of listeners) notify(tagged)
  }
  feedSource = source
}

function setFeedStatus(status: FeedStatus) {
  feedStatus = status
  for (const notify of feedStatusListeners) notify(status)
}

function mergeEvents(
  prev: Map<string, FeedEvent>,
  incoming: FeedEvent[],
): Map<string, FeedEvent> {
  let changed = false
  const next = new Map(prev)
  for (const ev of incoming) {
    if (!next.has(ev.id)) {
      next.set(ev.id, ev)
      changed = true
    }
  }
  if (!changed) return prev
  if (next.size <= FEED_CAP * 2) return next
  const kept = [...next.values()]
    .sort((a, b) => Number(b.id) - Number(a.id))
    .slice(0, FEED_CAP)
  return new Map(kept.map((ev) => [ev.id, ev]))
}
