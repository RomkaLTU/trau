import { describe, expect, it } from 'vitest'

import {
  autoNeverMatches,
  latestNoSkillsTicket,
  parseMatchers,
  scopeOf,
  skillPageUrl,
  toggleRequired,
  upsertRule,
  usageState,
  withoutRequired,
  type InstalledSkill,
  type SkillCoverage,
  type SkillRule,
} from './skills'
import type { FeedEvent } from './events'

const NOW = new Date('2026-07-24T12:00:00Z')

function daysAgo(days: number): string {
  return new Date(NOW.getTime() - days * 86_400_000).toISOString()
}

function ev(over: Partial<FeedEvent> & { id: string }): FeedEvent {
  return { ts: daysAgo(1), kind: 'build_no_skills', ...over }
}

function skill(over: Partial<InstalledSkill> & { name: string }): InstalledSkill {
  return { scope: 'auto', pinned: false, loads: 0, ...over }
}

function coverage(over: Partial<SkillCoverage> = {}): SkillCoverage {
  return { days: 30, has_data: true, phases: [], ...over }
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
    expect(latestNoSkillsTicket(events, NOW)).toBe('COD-9')
  })

  it('ignores events of other kinds', () => {
    const events = [
      ev({ id: '9', kind: 'phase_done', fields: { ticket: 'COD-9' } }),
      ev({ id: '3', fields: { ticket: 'COD-3' } }),
    ]
    expect(latestNoSkillsTicket(events, NOW)).toBe('COD-3')
  })

  it('skips a warning missing its ticket field', () => {
    const events = [ev({ id: '9', fields: {} }), ev({ id: '3', fields: { ticket: 'COD-3' } })]
    expect(latestNoSkillsTicket(events, NOW)).toBe('COD-3')
  })

  it('returns null when there are no warnings', () => {
    expect(latestNoSkillsTicket([ev({ id: '1', kind: 'phase_done' })], NOW)).toBeNull()
  })

  it('lets an old warning go stale instead of pinning a banner', () => {
    const events = [ev({ id: '9', ts: daysAgo(40), fields: { ticket: 'COD-9' } })]
    expect(latestNoSkillsTicket(events, NOW)).toBeNull()
  })

  it('falls back past a stale warning to a recent one', () => {
    const events = [
      ev({ id: '9', ts: daysAgo(40), fields: { ticket: 'COD-9' } }),
      ev({ id: '3', ts: daysAgo(2), fields: { ticket: 'COD-3' } }),
    ]
    expect(latestNoSkillsTicket(events, NOW)).toBe('COD-3')
  })
})

describe('scopeOf', () => {
  const rules: SkillRule[] = [
    { skill: 'web-feature', scope: 'auto', paths: ['web/**'] },
    { skill: 'github-release', scope: 'manual' },
  ]

  it('reads the scope from the skill’s own rule', () => {
    expect(scopeOf('web-feature', rules, [])).toBe('auto')
    expect(scopeOf('github-release', rules, [])).toBe('manual')
  })

  it('migrates a REQUIRED_SKILLS pin with no rule to always', () => {
    expect(scopeOf('golang-cli', rules, ['golang-cli'])).toBe('always')
  })

  it('leaves an unruled, unpinned skill on auto', () => {
    expect(scopeOf('golang-cli', rules, [])).toBe('auto')
  })

  it('lets an explicit rule win over the legacy pin', () => {
    expect(scopeOf('github-release', rules, ['github-release'])).toBe('manual')
  })
})

describe('upsertRule', () => {
  it('replaces a skill’s rule and keeps the list sorted', () => {
    const rules: SkillRule[] = [
      { skill: 'web-feature', scope: 'auto' },
      { skill: 'golang-cli', scope: 'always' },
    ]
    const next = upsertRule(rules, { skill: 'web-feature', scope: 'manual' })
    expect(next.map((r) => r.skill)).toEqual(['golang-cli', 'web-feature'])
    expect(next[1].scope).toBe('manual')
  })

  it('appends a skill that had no rule', () => {
    expect(upsertRule([], { skill: 'a', scope: 'always' })).toEqual([
      { skill: 'a', scope: 'always' },
    ])
  })
})

describe('withoutRequired', () => {
  it('drops the named pin and cleans the rest', () => {
    expect(withoutRequired([' a ', 'b', ''], 'b')).toBe('a')
  })

  it('is a no-op for a skill that was never pinned', () => {
    expect(withoutRequired(['a'], 'b')).toBe('a')
  })
})

describe('parseMatchers', () => {
  it('splits on commas and whitespace and drops blanks', () => {
    expect(parseMatchers(' web/**,  **/*.go\n internal/tui/** ')).toEqual([
      'web/**',
      '**/*.go',
      'internal/tui/**',
    ])
  })

  it('returns an empty list for blank input', () => {
    expect(parseMatchers('   ')).toEqual([])
  })
})

describe('autoNeverMatches', () => {
  it('flags an auto rule with neither paths nor keywords', () => {
    expect(autoNeverMatches({ skill: 'a', scope: 'auto' })).toBe(true)
  })

  it('accepts an auto rule with a matcher', () => {
    expect(autoNeverMatches({ skill: 'a', scope: 'auto', paths: ['web/**'] })).toBe(false)
  })

  it('does not apply to always or manual rules', () => {
    expect(autoNeverMatches({ skill: 'a', scope: 'always' })).toBe(false)
    expect(autoNeverMatches({ skill: 'a', scope: 'manual' })).toBe(false)
    expect(autoNeverMatches(undefined)).toBe(false)
  })
})

describe('usageState', () => {
  it('reads a loaded skill from its load count', () => {
    expect(usageState(skill({ name: 'a', loads: 3 }), coverage())).toBe('loaded')
  })

  it('calls an unloaded skill dead only when the runs could have reported it', () => {
    expect(usageState(skill({ name: 'a' }), coverage())).toBe('dead')
  })

  it('reads unknown when no run carried recoverable evidence', () => {
    expect(usageState(skill({ name: 'a' }), coverage({ has_data: false }))).toBe('unknown')
  })

  it('keeps a loaded skill loaded even when later runs went silent', () => {
    expect(
      usageState(skill({ name: 'a', loads: 1 }), coverage({ has_data: false })),
    ).toBe('loaded')
  })
})
