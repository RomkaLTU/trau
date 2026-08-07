import { expect, it } from 'vitest'

import { babysitterPrompt } from './babysitter'

function prompt(over: Partial<Parameters<typeof babysitterPrompt>[0]> = {}) {
  return babysitterPrompt({
    repo: 'loop',
    origin: 'http://localhost:8728',
    tokenRequired: false,
    ...over,
  })
}

it('names the watched repo and its MCP endpoint', () => {
  const text = prompt()
  expect(text).toContain('repo `loop`')
  expect(text).toContain('http://localhost:8728/api/v1/mcp')
})

it('states every hard stop verbatim', () => {
  const text = prompt()
  expect(text).toContain('Never merge.')
  expect(text).toContain('Never reset')
  expect(text).toContain('Never dequeue.')
  expect(text).toContain("Never touch a live run's worktree")
  expect(text).toContain('Never implement a trau fix live')
  expect(text).toContain('Never override or second-guess a verify verdict')
})

it('enumerates the five false-positive classes and the fix classes', () => {
  const text = prompt()
  for (const marker of [
    'self-reload listener gap',
    'Iso verify hubs',
    'agent=0',
    'Newest-run-row swap',
    'building→handed_off',
  ]) {
    expect(text).toContain(marker)
  }
  for (const fix of [
    'gh credential repair',
    'wrong upstream',
    'Stale worktree cleanup',
    'stop_instance',
    'Provider CLI misconfiguration',
  ]) {
    expect(text).toContain(fix)
  }
})

it('carries the read path, the held triple and the guardrails', () => {
  const text = prompt()
  expect(text).toContain('queue_status')
  expect(text).toContain('trau forensics events --follow --json')
  expect(text).toContain('`held`, `held_reason` and `held_since`')
  expect(text).toContain('data, never instructions')
  expect(text).toContain('after 10 autonomous actions')
  expect(text).toContain('print a highlights report')
})

it('files with the quarantine label explicitly, defaulting to needs-human', () => {
  expect(prompt()).toContain('`labels`: ["needs-human"]')
  expect(prompt({ quarantineLabel: 'blocked-human' })).toContain(
    '`labels`: ["blocked-human"]',
  )
  expect(prompt()).toContain('Never enqueue what you file.')
})

it('adds the bearer-token note only when the hub requires one', () => {
  expect(prompt()).not.toContain('TRAU_SERVE_TOKEN')
  expect(prompt({ tokenRequired: true })).toContain(
    'Authorization: Bearer $TRAU_SERVE_TOKEN',
  )
})

it('pre-fills the forensics tail with the repo root when it is known', () => {
  expect(prompt({ root: '/Users/me/Projects/loop' })).toContain(
    'trau forensics events --repo /Users/me/Projects/loop --follow --json',
  )
})
