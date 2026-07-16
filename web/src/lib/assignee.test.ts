import { describe, expect, it } from 'vitest'

import {
  assigneeLabel,
  avatarInitials,
  avatarTone,
  initials,
  type Assignee,
} from './assignee'

const named = (name: string, me = false): Assignee => ({ id: name, name, me })

describe('initials', () => {
  it('takes first and last initials from a full name', () => {
    expect(initials('Ada Lovelace')).toBe('AL')
    expect(initials('  grace   brewster   hopper ')).toBe('GH')
  })

  it('takes up to two letters from a single name', () => {
    expect(initials('Romka')).toBe('RO')
    expect(initials('x')).toBe('X')
  })

  it('falls back to ? for a blank name', () => {
    expect(initials('   ')).toBe('?')
  })
})

describe('assigneeLabel / avatarInitials', () => {
  it('collapses the repo identity to Me, never You', () => {
    const me = named('Ada Lovelace', true)
    expect(assigneeLabel(me)).toBe('Me')
    expect(avatarInitials(me)).toBe('Me')
    expect(assigneeLabel(me)).not.toMatch(/you/i)
  })

  it('shows the real name and its initials for anyone else', () => {
    const other = named('Ada Lovelace')
    expect(assigneeLabel(other)).toBe('Ada Lovelace')
    expect(avatarInitials(other)).toBe('AL')
  })
})

describe('avatarTone', () => {
  it('is deterministic for a given name', () => {
    expect(avatarTone('Ada Lovelace')).toBe(avatarTone('Ada Lovelace'))
  })

  it('returns a background utility class', () => {
    expect(avatarTone('Ada Lovelace')).toMatch(/^bg-/)
  })
})
