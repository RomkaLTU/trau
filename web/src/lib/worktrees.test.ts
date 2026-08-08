import { describe, expect, it } from 'vitest'

import type { Worktree } from './rundetail'
import {
  activeWorktrees,
  appActionAvailable,
  appControlBlocked,
  worktreeAge,
} from './worktrees'

function tree(over: Partial<Worktree> = {}): Worktree {
  return {
    id: 1,
    repo: 'acme',
    ticket: 'COD-1585',
    path: '/tmp/worktrees/acme/COD-1585',
    branch: 'feature/COD-1585',
    state: 'active',
    serving: true,
    app_state: 'stopped',
    ...over,
  }
}

describe('activeWorktrees', () => {
  it('keeps only the trees still on disk', () => {
    const rows = [
      tree({ id: 1 }),
      tree({ id: 2, state: 'settled' }),
      tree({ id: 3, state: 'orphaned' }),
    ]
    expect(activeWorktrees(rows).map((row) => row.id)).toEqual([1])
  })
})

describe('appControlBlocked', () => {
  it('allows the controls on a standing tree nothing is working in', () => {
    expect(appControlBlocked(tree())).toBe('')
  })

  it('names the run holding the tree', () => {
    expect(appControlBlocked(tree({ busy: 'COD-9 is mid-verifying in this tree' }))).toBe(
      'COD-9 is mid-verifying in this tree',
    )
  })

  it('refuses a tree that has left the disk', () => {
    expect(appControlBlocked(tree({ state: 'settled' }))).toContain('no longer on disk')
  })

  it('refuses a repo that serves no app', () => {
    expect(appControlBlocked(tree({ serving: false }))).toContain('APP_START_CMD')
  })
})

describe('worktreeAge', () => {
  const now = Date.parse('2026-08-08T12:00:00Z')

  it('reads a fresh tree as just created', () => {
    expect(worktreeAge('2026-08-08T11:59:40Z', now)).toBe('created just now')
  })

  it('reads an older tree as an age', () => {
    expect(worktreeAge('2026-08-08T11:35:00Z', now)).toBe('created 25m ago')
    expect(worktreeAge('2026-08-06T12:00:00Z', now)).toBe('created 2d ago')
  })

  it('hands back a stamp it cannot read', () => {
    expect(worktreeAge('not a date', now)).toBe('not a date')
  })
})

describe('appActionAvailable', () => {
  it('offers Start only while the app is down', () => {
    expect(appActionAvailable(tree({ app_state: 'stopped' }), 'start')).toBe(true)
    expect(appActionAvailable(tree({ app_state: 'failed' }), 'start')).toBe(true)
    expect(appActionAvailable(tree({ app_state: 'running' }), 'start')).toBe(false)
  })

  it('offers Stop and Restart only while something is up', () => {
    expect(appActionAvailable(tree({ app_state: 'running' }), 'stop')).toBe(true)
    expect(appActionAvailable(tree({ app_state: 'starting' }), 'restart')).toBe(true)
    expect(appActionAvailable(tree({ app_state: 'stopped' }), 'stop')).toBe(false)
  })
})
