import { describe, expect, it } from 'vitest'

import type { ConfigKey } from '@/lib/config'
import {
  appliesOnHubRestart,
  deriveSections,
  displayValue,
  isModified,
  matchesQuery,
  sectionSlug,
} from '@/lib/settings'

function key(overrides: Partial<ConfigKey> & { key: string }): ConfigKey {
  return {
    value: '',
    layer: 'default',
    editable: false,
    ...overrides,
  }
}

describe('deriveSections', () => {
  it('groups keys by their server group and orders sections by catalog order', () => {
    const sections = deriveSections([
      key({ key: 'THEME', group: 'TUI & notifications' }),
      key({ key: 'LINEAR_TEAM', group: 'Tracker & issues' }),
      key({ key: 'BASE_BRANCH', group: 'Git & merge' }),
      key({ key: 'ISSUE_PREFIX', group: 'Tracker & issues' }),
    ])

    expect(sections.map((s) => s.group)).toEqual([
      'Tracker & issues',
      'Git & merge',
      'TUI & notifications',
    ])
    expect(sections[0].keys.map((k) => k.key)).toEqual([
      'LINEAR_TEAM',
      'ISSUE_PREFIX',
    ])
  })

  it('buckets unknown or missing groups into an Other section rather than dropping keys', () => {
    const sections = deriveSections([
      key({ key: 'MYSTERY', group: 'Something New' }),
      key({ key: 'NO_GROUP' }),
      key({ key: 'LINEAR_TEAM', group: 'Tracker & issues' }),
    ])

    const groups = sections.map((s) => s.group)
    expect(groups[0]).toBe('Tracker & issues')
    expect(groups[groups.length - 1]).toBe('Other')

    const other = sections.find((s) => s.group === 'Other')!
    expect(other.keys.map((k) => k.key)).toEqual(['MYSTERY', 'NO_GROUP'])
  })

  it('splits advanced keys out of the primary list', () => {
    const [section] = deriveSections([
      key({ key: 'A', group: 'CI' }),
      key({ key: 'B', group: 'CI', advanced: true }),
    ])
    expect(section.primaryKeys.map((k) => k.key)).toEqual(['A'])
    expect(section.advancedKeys.map((k) => k.key)).toEqual(['B'])
  })

  it('marks a section modified when any of its keys are overridden', () => {
    const sections = deriveSections([
      key({ key: 'A', group: 'CI' }),
      key({ key: 'B', group: 'Git & merge', layer: 'project' }),
    ])
    const byGroup = Object.fromEntries(sections.map((s) => [s.group, s.modified]))
    expect(byGroup['CI']).toBe(false)
    expect(byGroup['Git & merge']).toBe(true)
  })

  it('flags hub-read sections as applying on hub restart', () => {
    const sections = deriveSections([
      key({ key: 'SERVE_PORT', group: 'Hub & web server' }),
      key({ key: 'EVENT_RETENTION', group: 'Retention' }),
      key({ key: 'LINEAR_TEAM', group: 'Tracker & issues' }),
    ])
    const byGroup = Object.fromEntries(sections.map((s) => [s.group, s.hubRestart]))
    expect(byGroup['Hub & web server']).toBe(true)
    expect(byGroup['Retention']).toBe(true)
    expect(byGroup['Tracker & issues']).toBe(false)
  })
})

describe('isModified', () => {
  it('treats a non-default layer as modified', () => {
    expect(isModified(key({ key: 'A', layer: 'project' }))).toBe(true)
    expect(isModified(key({ key: 'A', layer: 'default' }))).toBe(false)
  })

  it('requires a secret to be set and non-default', () => {
    expect(isModified(key({ key: 'S', secret: true, set: true, layer: 'local' }))).toBe(true)
    expect(isModified(key({ key: 'S', secret: true, set: true, layer: 'default' }))).toBe(false)
    expect(isModified(key({ key: 'S', secret: true, layer: 'local' }))).toBe(false)
  })
})

describe('displayValue', () => {
  it('masks secrets and shows a dash when unset', () => {
    expect(displayValue(key({ key: 'S', secret: true, set: true }))).toBe('••••••••')
    expect(displayValue(key({ key: 'S', secret: true }))).toBe('—')
  })

  it('renders bools as on/off and blanks as a dash', () => {
    expect(displayValue(key({ key: 'B', bool: true, value: '1' }))).toBe('on')
    expect(displayValue(key({ key: 'B', bool: true, value: '0' }))).toBe('off')
    expect(displayValue(key({ key: 'V', value: '' }))).toBe('—')
    expect(displayValue(key({ key: 'V', value: 'origin' }))).toBe('origin')
  })
})

describe('matchesQuery', () => {
  const k = key({ key: 'BASE_BRANCH', description: 'Branch that features fork from.' })

  it('matches on key or description, case-insensitively', () => {
    expect(matchesQuery(k, 'base')).toBe(true)
    expect(matchesQuery(k, 'FORK')).toBe(true)
    expect(matchesQuery(k, 'nope')).toBe(false)
  })

  it('matches everything on an empty query', () => {
    expect(matchesQuery(k, '')).toBe(true)
  })
})

describe('sectionSlug', () => {
  it('produces stable url-safe anchors', () => {
    expect(sectionSlug('Tracker & issues')).toBe('tracker-issues')
    expect(sectionSlug('CI')).toBe('ci')
    expect(sectionSlug('Hub & web server')).toBe('hub-web-server')
  })
})

describe('appliesOnHubRestart', () => {
  it('is true only for hub and retention groups', () => {
    expect(appliesOnHubRestart('Hub & web server')).toBe(true)
    expect(appliesOnHubRestart('Retention')).toBe(true)
    expect(appliesOnHubRestart('CI')).toBe(false)
  })
})
