import { expect, it } from 'vitest'

import {
  matchActions,
  paletteActions,
  updateCheckMessage,
  type ActionContext,
  type PaletteActionId,
} from './palette-actions'
import type { UpdateStatus } from './update'

function ids(overrides: Partial<ActionContext> = {}): PaletteActionId[] {
  return paletteActions({
    repo: 'loop',
    draining: false,
    provider: 'linear',
    theme: 'light',
    ...overrides,
  }).map((action) => action.id)
}

it('offers Start while the queue sits idle and Stop while it drains', () => {
  expect(ids()).toContain('start-loop')
  expect(ids()).not.toContain('stop-loop')

  expect(ids({ draining: true })).toContain('stop-loop')
  expect(ids({ draining: true })).not.toContain('start-loop')
})

it('offers the repo-scoped rows when no repo is scoped to describe', () => {
  const unscoped = ids({ repo: '', draining: undefined, provider: undefined })

  expect(unscoped).toContain('start-loop')
  expect(unscoped).toContain('sync-backlog')
})

it('holds the repo-scoped rows back until the repo’s state lands', () => {
  const loading = ids({ draining: undefined, provider: undefined })

  expect(loading).not.toContain('start-loop')
  expect(loading).not.toContain('stop-loop')
  expect(loading).not.toContain('sync-backlog')
})

it('drops Sync backlog for a repo with no external tracker', () => {
  expect(ids({ provider: 'internal' })).not.toContain('sync-backlog')
  expect(ids({ provider: '' })).not.toContain('sync-backlog')
})

it('marks the repo-scoped actions as gated', () => {
  const gated = paletteActions({
    repo: 'loop',
    draining: false,
    provider: 'linear',
    theme: 'light',
  })
    .filter((action) => action.requiresProject)
    .map((action) => action.id)

  expect(gated).toEqual(['start-loop', 'sync-backlog', 'run-next'])
})

it('labels the theme action with the mode it switches to', () => {
  const label = (theme: 'light' | 'dark') =>
    paletteActions({ repo: 'loop', draining: false, theme }).find(
      (action) => action.id === 'toggle-theme',
    )?.label

  expect(label('light')).toBe('Switch to dark mode')
  expect(label('dark')).toBe('Switch to light mode')
})

it('matches actions by name and by what they are about', () => {
  const actions = paletteActions({
    repo: 'loop',
    draining: false,
    provider: 'linear',
    theme: 'light',
  })
  const matched = (query: string) =>
    matchActions(actions, query).map((action) => action.id)

  expect(matched('start')).toEqual(['start-loop'])
  expect(matched('sync')).toEqual(['sync-backlog'])
  expect(matched('theme')).toEqual(['toggle-theme'])
  expect(matched('nothing here')).toEqual([])
  expect(matched('')).toHaveLength(actions.length)
})

it('says what the update check found', () => {
  const status = (fields: Partial<UpdateStatus>) =>
    ({ running: '0.9.0', latest: '0.9.0', ...fields }) as UpdateStatus

  expect(updateCheckMessage(status({ updateAvailable: true, latest: '1.0.0' }))).toBe(
    'Update available — v1.0.0',
  )
  expect(updateCheckMessage(status({ updateAvailable: false }))).toBe(
    'Up to date — v0.9.0',
  )
})
