import { describe, expect, it } from 'vitest'

import type { RepoFeedEvent } from '@/lib/events'
import {
  deriveRecap,
  describeRecapItem,
  pauseReason,
  toRecapItem,
} from '@/lib/recap'

function sc(
  repo: string,
  id: string,
  ts: string,
  state: string,
  reason = '',
  ticket = '',
): RepoFeedEvent {
  return { repo, id, ts, kind: 'state_change', fields: { state, reason, ticket } }
}

// A cross-repo history straddling the visit marker: everything at or before
// 10:00 predates the last visit; everything after is part of the away window.
const HISTORY: RepoFeedEvent[] = [
  sc('salonradar', '10', '2026-07-04T09:30:00Z', 'merged', '', 'COD-100'),
  sc('salonradar', '20', '2026-07-04T10:00:00Z', 'merged', '', 'COD-101'),
  sc('salonradar', '30', '2026-07-04T10:05:00Z', 'merged', '', 'COD-200'),
  sc('salonradar', '40', '2026-07-04T10:10:00Z', 'paused', 'usage_window', 'COD-201'),
  sc('m4c-api', '10', '2026-07-04T10:12:00Z', 'paused', 'reauth', 'M4C-77'),
  sc('m4c-api', '20', '2026-07-04T10:15:00Z', 'faulted', 'build', 'M4C-78'),
  sc('trucknet', '10', '2026-07-04T10:20:00Z', 'quarantined', 'pre-push hook rejected', 'TMS-9'),
  { repo: 'salonradar', id: '99', ts: '2026-07-04T10:25:00Z', kind: 'agent_call', fields: {} },
  sc('salonradar', '50', '2026-07-04T10:30:00Z', 'exploded', '', 'COD-300'),
]

const SINCE = '2026-07-04T10:00:00Z'

describe('deriveRecap', () => {
  it('buckets each state change after the marker by its terminal state', () => {
    const recap = deriveRecap(HISTORY, SINCE)

    expect(recap.merged.map((i) => i.ticket)).toEqual(['COD-200'])
    expect(recap.paused.map((i) => i.ticket)).toEqual(['M4C-77', 'COD-201'])
    expect(recap.faulted.map((i) => i.ticket)).toEqual(['M4C-78'])
    expect(recap.quarantined.map((i) => i.ticket)).toEqual(['TMS-9'])
    expect(recap.total).toBe(5)
  })

  it('excludes events at or before the marker', () => {
    const recap = deriveRecap(HISTORY, SINCE)
    const tickets = recap.merged.map((i) => i.ticket)
    expect(tickets).not.toContain('COD-100')
    expect(tickets).not.toContain('COD-101')
  })

  it('ignores non state_change events and unknown states', () => {
    const recap = deriveRecap(HISTORY, SINCE)
    const seen = [
      ...recap.merged,
      ...recap.paused,
      ...recap.faulted,
      ...recap.quarantined,
    ].map((i) => i.ticket)
    expect(seen).not.toContain('COD-300')
    expect(recap.total).toBe(5)
  })

  it('distinguishes a usage-window pause from a re-auth pause', () => {
    const recap = deriveRecap(HISTORY, SINCE)
    const byTicket = Object.fromEntries(recap.paused.map((i) => [i.ticket, i]))
    expect(pauseReason(byTicket['COD-201'])).toBe('usage_window')
    expect(pauseReason(byTicket['M4C-77'])).toBe('reauth')
  })

  it('orders each bucket newest first', () => {
    const events = [
      sc('r', '1', '2026-07-04T11:00:00Z', 'merged', '', 'A'),
      sc('r', '2', '2026-07-04T11:02:00Z', 'merged', '', 'B'),
      sc('r', '3', '2026-07-04T11:01:00Z', 'merged', '', 'C'),
    ]
    const recap = deriveRecap(events, SINCE)
    expect(recap.merged.map((i) => i.ticket)).toEqual(['B', 'C', 'A'])
  })

  it('dedupes repeated events by repo-qualified key', () => {
    const dup = sc('r', '1', '2026-07-04T11:00:00Z', 'merged', '', 'A')
    const recap = deriveRecap([dup, dup], SINCE)
    expect(recap.merged).toHaveLength(1)
  })

  it('does not collide identical event ids from different repos', () => {
    const events = [
      sc('salonradar', '1', '2026-07-04T11:00:00Z', 'merged', '', 'COD-1'),
      sc('m4c-api', '1', '2026-07-04T11:01:00Z', 'merged', '', 'M4C-1'),
    ]
    const recap = deriveRecap(events, SINCE)
    expect(recap.merged.map((i) => i.ticket).sort()).toEqual(['COD-1', 'M4C-1'])
  })

  it('treats a null marker as no lower bound', () => {
    const recap = deriveRecap(HISTORY, null)
    expect(recap.merged.map((i) => i.ticket)).toEqual(['COD-200', 'COD-101', 'COD-100'])
  })

  it('compares by instant, not string, across timezone offsets', () => {
    const marker = '2026-07-04T10:00:00Z'
    const before = sc('r', '1', '2026-07-04T12:59:00+03:00', 'merged', '', 'BEFORE')
    const after = sc('r', '2', '2026-07-04T13:01:00+03:00', 'merged', '', 'AFTER')
    const recap = deriveRecap([before, after], marker)
    expect(recap.merged.map((i) => i.ticket)).toEqual(['AFTER'])
  })
})

describe('describeRecapItem', () => {
  const item = (state: string, reason = '', ticket = 'COD-1', repo = 'salonradar') =>
    toRecapItem(sc(repo, '1', '2026-07-04T10:00:00Z', state, reason, ticket))!

  it('phrases each state, naming the pause reason', () => {
    expect(describeRecapItem(item('merged'))).toBe('COD-1 merged')
    expect(describeRecapItem(item('faulted', 'verify'))).toBe(
      'COD-1 faulted during verify',
    )
    expect(describeRecapItem(item('quarantined', 'no green CI'))).toBe(
      'COD-1 quarantined — no green CI',
    )
    expect(describeRecapItem(item('paused', 'usage_window'))).toBe(
      'COD-1 paused — usage window',
    )
    expect(describeRecapItem(item('paused', 'reauth'))).toBe(
      'COD-1 paused — needs re-auth',
    )
  })

  it('falls back to the repo when no ticket is tagged', () => {
    expect(describeRecapItem(item('merged', '', ''))).toBe('salonradar merged')
  })
})
