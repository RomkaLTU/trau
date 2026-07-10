import { describe, expect, it } from 'vitest'

import { latestNoSkillsTicket, skillPageUrl, toggleRequired } from './skills'
import type { FeedEvent } from './events'

function ev(over: Partial<FeedEvent> & { id: string }): FeedEvent {
  return { ts: '', kind: 'build_no_skills', ...over }
}

describe('toggleRequired', () => {
  it('pins a skill not yet required', () => {
    expect(toggleRequired(['a'], 'b')).toBe('a,b')
  })

  it('unpins an already-required skill', () => {
    expect(toggleRequired(['a', 'b', 'c'], 'b')).toBe('a,c')
  })

  it('pins into an empty list', () => {
    expect(toggleRequired([], 'a')).toBe('a')
  })

  it('empties the list when unpinning the last skill', () => {
    expect(toggleRequired(['a'], 'a')).toBe('')
  })

  it('trims and drops blanks so the persisted value stays clean', () => {
    expect(toggleRequired([' a ', '', 'b'], 'c')).toBe('a,b,c')
  })
})

describe('skillPageUrl', () => {
  it('builds a skills.sh page from the lock source', () => {
    expect(skillPageUrl('samber/cc-skills-golang')).toBe(
      'https://skills.sh/samber/cc-skills-golang',
    )
  })

  it('returns null for a hand-dropped skill with no source', () => {
    expect(skillPageUrl(undefined)).toBeNull()
    expect(skillPageUrl('  ')).toBeNull()
  })
})

describe('latestNoSkillsTicket', () => {
  it('returns the most recent warning ticket by id', () => {
    const events = [
      ev({ id: '5', fields: { ticket: 'COD-5' } }),
      ev({ id: '9', fields: { ticket: 'COD-9' } }),
      ev({ id: '7', fields: { ticket: 'COD-7' } }),
    ]
    expect(latestNoSkillsTicket(events)).toBe('COD-9')
  })

  it('ignores events of other kinds', () => {
    const events = [
      ev({ id: '9', kind: 'phase_done', fields: { ticket: 'COD-9' } }),
      ev({ id: '3', fields: { ticket: 'COD-3' } }),
    ]
    expect(latestNoSkillsTicket(events)).toBe('COD-3')
  })

  it('skips a warning missing its ticket field', () => {
    const events = [ev({ id: '9', fields: {} }), ev({ id: '3', fields: { ticket: 'COD-3' } })]
    expect(latestNoSkillsTicket(events)).toBe('COD-3')
  })

  it('returns null when there are no warnings', () => {
    expect(latestNoSkillsTicket([ev({ id: '1', kind: 'phase_done' })])).toBeNull()
  })
})
