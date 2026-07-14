import { describe, expect, it } from 'vitest'

import type { ConfigKey } from '@/lib/config'
import {
  appliesOnHubRestart,
  canResetLayer,
  comboboxFreeEntry,
  derivePhaseMatrix,
  deriveSections,
  displayValue,
  editorVariant,
  isHexColor,
  isModified,
  matchesQuery,
  routingCellKey,
  sectionSlug,
  shadowNote,
  themeRoleLabel,
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
    const byGroup = Object.fromEntries(
      sections.map((s) => [s.group, s.modified]),
    )
    expect(byGroup['CI']).toBe(false)
    expect(byGroup['Git & merge']).toBe(true)
  })

  it('flags hub-read sections as applying on hub restart', () => {
    const sections = deriveSections([
      key({ key: 'SERVE_PORT', group: 'Hub & web server' }),
      key({ key: 'EVENT_RETENTION', group: 'Retention' }),
      key({ key: 'LINEAR_TEAM', group: 'Tracker & issues' }),
    ])
    const byGroup = Object.fromEntries(
      sections.map((s) => [s.group, s.hubRestart]),
    )
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
    expect(
      isModified(key({ key: 'S', secret: true, set: true, layer: 'local' })),
    ).toBe(true)
    expect(
      isModified(key({ key: 'S', secret: true, set: true, layer: 'default' })),
    ).toBe(false)
    expect(isModified(key({ key: 'S', secret: true, layer: 'local' }))).toBe(
      false,
    )
  })
})

describe('canResetLayer', () => {
  it('allows reset only for the two layers the web can write', () => {
    expect(canResetLayer('project')).toBe(true)
    expect(canResetLayer('user')).toBe(true)
    expect(canResetLayer('default')).toBe(false)
    expect(canResetLayer('env var')).toBe(false)
    expect(canResetLayer('CLI')).toBe(false)
    expect(canResetLayer('local')).toBe(false)
  })
})

describe('shadowNote', () => {
  it('warns that an env var shadows either write target', () => {
    expect(shadowNote('env var', 'project')).toBe(
      "set via env var — this write won't take effect while it's set",
    )
    expect(shadowNote('env var', 'user')).toBe(
      "set via env var — this write won't take effect while it's set",
    )
  })

  it('warns that a CLI override shadows either write target', () => {
    expect(shadowNote('CLI', 'project')).toBe(
      "set via CLI — this write won't take effect while it's set",
    )
    expect(shadowNote('CLI', 'user')).toBe(
      "set via CLI — this write won't take effect while it's set",
    )
  })

  it('steers a project-target write to user when user already overrides it', () => {
    expect(shadowNote('user', 'project')).toBe(
      'user layer overrides project — write to user instead',
    )
  })

  it('stays silent when the target outranks or equals the effective layer', () => {
    expect(shadowNote('project', 'project')).toBeNull()
    expect(shadowNote('user', 'user')).toBeNull()
    expect(shadowNote('project', 'user')).toBeNull()
    expect(shadowNote('default', 'project')).toBeNull()
    expect(shadowNote('local', 'project')).toBeNull()
  })
})

describe('displayValue', () => {
  it('masks secrets and shows a dash when unset', () => {
    expect(displayValue(key({ key: 'S', secret: true, set: true }))).toBe(
      '••••••••',
    )
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
  const k = key({
    key: 'BASE_BRANCH',
    description: 'Branch that features fork from.',
  })

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

describe('derivePhaseMatrix', () => {
  const routingKeys = [
    key({ key: 'CLAUDE_BUILD_MODEL' }),
    key({ key: 'CLAUDE_BUILD_EFFORT' }),
    key({ key: 'CLAUDE_CLEANUP_MODEL' }),
    key({ key: 'CLAUDE_BUILD_DISALLOWED_TOOLS' }),
    key({ key: 'CODEX_BUILD_MODEL' }),
    key({ key: 'CODEX_BUILD_EFFORT' }),
    key({ key: 'KIMI_BUILD_MODEL' }),
    key({ key: 'KIMI_VERIFY_MODEL' }),
  ]

  it('derives providers, phases, and columns from catalog keys only', () => {
    const model = derivePhaseMatrix(routingKeys)
    expect(model.providers).toEqual(['CLAUDE', 'CODEX', 'KIMI'])
    expect(model.phases.CLAUDE).toEqual(['BUILD', 'CLEANUP'])
    expect(model.phases.KIMI).toEqual(['BUILD', 'VERIFY'])
  })

  it('gives each provider only the columns whose keys exist', () => {
    const model = derivePhaseMatrix(routingKeys)
    expect(model.columns.CLAUDE).toEqual([
      'MODEL',
      'EFFORT',
      'DISALLOWED_TOOLS',
    ])
    expect(model.columns.CODEX).toEqual(['MODEL', 'EFFORT'])
    expect(model.columns.KIMI).toEqual(['MODEL'])
  })

  it('ignores keys that are not phase-routing keys', () => {
    const model = derivePhaseMatrix([
      key({ key: 'CLAUDE_MODEL' }),
      key({ key: 'THEME' }),
      key({ key: 'CLAUDE_BUILD_MODEL' }),
    ])
    expect(model.providers).toEqual(['CLAUDE'])
    expect(model.phases.CLAUDE).toEqual(['BUILD'])
  })
})

describe('routingCellKey', () => {
  it('rebuilds the catalog key for a provider/phase/column cell', () => {
    expect(routingCellKey('CLAUDE', 'BUILD', 'DISALLOWED_TOOLS')).toBe(
      'CLAUDE_BUILD_DISALLOWED_TOOLS',
    )
  })
})

describe('themeRoleLabel', () => {
  it('strips the THEME_ prefix and lowercases the role', () => {
    expect(themeRoleLabel('THEME_ACCENT')).toBe('accent')
    expect(themeRoleLabel('THEME_BORDER')).toBe('border')
  })
})

describe('isHexColor', () => {
  it('accepts #rrggbb and rejects anything else', () => {
    expect(isHexColor('#7d56f4')).toBe(true)
    expect(isHexColor('#7D56F4')).toBe(true)
    expect(isHexColor('7d56f4')).toBe(false)
    expect(isHexColor('#7d56f')).toBe(false)
    expect(isHexColor('#7d56f4f')).toBe(false)
    expect(isHexColor('')).toBe(false)
  })
})

describe('editorVariant', () => {
  it('routes model keys with suggestions to the combobox', () => {
    expect(
      editorVariant(
        key({ key: 'CLAUDE_MODEL', suggestions: ['claude-opus', 'claude-sonnet'] }),
      ),
    ).toBe('combobox')
    expect(
      editorVariant(key({ key: 'GRILL_MODEL', suggestions: ['claude-opus'] })),
    ).toBe('combobox')
  })

  it('renders effort keys as a strict select from their options, not a combobox', () => {
    expect(
      editorVariant(
        key({ key: 'CLAUDE_EFFORT', options: ['low', 'medium', 'high'] }),
      ),
    ).toBe('select')
  })

  it('falls back to free text when suggestions are empty', () => {
    expect(editorVariant(key({ key: 'KIMI_MODEL', suggestions: [] }))).toBe('text')
    expect(editorVariant(key({ key: 'KIMI_MODEL' }))).toBe('text')
  })

  it('keeps bool and color keys on their own variants', () => {
    expect(editorVariant(key({ key: 'AUTO_MERGE', bool: true }))).toBe('bool')
    expect(editorVariant(key({ key: 'THEME_ACCENT', kind: 'color' }))).toBe('color')
  })
})

describe('comboboxFreeEntry', () => {
  const models = ['claude-opus-4', 'claude-sonnet-4']

  it('offers a trimmed custom id that is not already a suggestion', () => {
    expect(comboboxFreeEntry('  gpt-5-mini ', models)).toBe('gpt-5-mini')
  })

  it('suppresses the free entry when the query matches a suggestion exactly', () => {
    expect(comboboxFreeEntry('claude-opus-4', models)).toBeNull()
  })

  it('offers nothing for a blank query', () => {
    expect(comboboxFreeEntry('', models)).toBeNull()
    expect(comboboxFreeEntry('   ', models)).toBeNull()
  })
})
