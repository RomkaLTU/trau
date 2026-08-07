import { describe, expect, it } from 'vitest'

import type { ConfigKey } from '@/lib/config'
import {
  activeTracker,
  appliesOnHubRestart,
  canResetLayer,
  comboboxFreeEntry,
  derivePhaseMatrix,
  deriveSections,
  displayValue,
  editorVariant,
  inactiveTrackerNote,
  isInactiveTrackerKey,
  isHexColor,
  isModified,
  matchSettings,
  matchesQuery,
  parseSettingsSearch,
  routingCell,
  routingCellKey,
  sectionSlug,
  shadowNote,
  themeRoleLabel,
  trackerHint,
  valueWarning,
  visibleKeys,
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

describe('routingCell', () => {
  it('shows an explicit value with its layer', () => {
    expect(
      routingCell(
        key({ key: 'CLAUDE_BUILD_MODEL', value: 'sonnet', layer: 'user' }),
      ),
    ).toEqual({ value: 'sonnet', explicit: true })
  })

  it('falls back to the catalog default when nothing is set', () => {
    expect(
      routingCell(key({ key: 'CLAUDE_BUILD_MODEL', default: 'opus' })),
    ).toEqual({ value: 'opus', explicit: false })
  })

  it('is empty for a phase whose provider CLI picks for itself', () => {
    expect(routingCell(key({ key: 'CLAUDE_BUILD_EFFORT' }))).toEqual({
      value: '',
      explicit: false,
    })
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

describe('valueWarning', () => {
  it('warns that BROWSER_VERIFY=never can ship broken functionality', () => {
    const warning = valueWarning('BROWSER_VERIFY', 'never')
    expect(warning).toMatch(/real browser/)
    expect(warning).toMatch(/ship undetected/)
  })

  it('stays silent for the browser verify values that still drive a browser', () => {
    expect(valueWarning('BROWSER_VERIFY', 'auto')).toBeNull()
    expect(valueWarning('BROWSER_VERIFY', 'always')).toBeNull()
    expect(valueWarning('BROWSER_VERIFY', '')).toBeNull()
  })

  it('does not leak the warning onto other keys holding the same value', () => {
    expect(valueWarning('VERIFY_PROOFS', 'never')).toBeNull()
    expect(valueWarning('TIMELOG_STORAGE', 'none')).toBeNull()
  })
})

describe('comboboxFreeEntry', () => {
  const models = ['claude-opus-5', 'claude-sonnet-5']

  it('offers a trimmed custom id that is not already a suggestion', () => {
    expect(comboboxFreeEntry('  gpt-5-mini ', models)).toBe('gpt-5-mini')
  })

  it('suppresses the free entry when the query matches a suggestion exactly', () => {
    expect(comboboxFreeEntry('claude-opus-5', models)).toBeNull()
  })

  it('offers nothing for a blank query', () => {
    expect(comboboxFreeEntry('', models)).toBeNull()
    expect(comboboxFreeEntry('   ', models)).toBeNull()
  })
})

describe('visibleKeys', () => {
  it('drops the app URL fallback keys, which the App URLs page now owns', () => {
    const kept = visibleKeys([
      key({ key: 'APP_URL', group: 'Verification' }),
      key({ key: 'APP_URLS', group: 'Verification' }),
      key({ key: 'BROWSER_VERIFY', group: 'Verification' }),
    ])

    expect(kept.map((k) => k.key)).toEqual(['BROWSER_VERIFY'])
  })
})

describe('matchSettings', () => {
  const keys = [
    key({ key: 'PROVIDER', value: 'claude', group: 'Providers & models' }),
    key({ key: 'PROVIDER_CLI', value: 'claude', group: 'Providers & models' }),
    key({
      key: 'LINEAR_API_KEY',
      group: 'Tracker & issues',
      secret: true,
      set: true,
    }),
    key({
      key: 'BASE_BRANCH',
      value: 'main',
      group: 'Git & merge',
      description: 'branch every provider run merges into',
    }),
  ]

  it('matches key names and descriptions, tagged with their section', () => {
    expect(matchSettings(keys, 'provider')).toEqual([
      { item: keys[3], section: 'Git & merge' },
      { item: keys[0], section: 'Providers & models' },
      { item: keys[1], section: 'Providers & models' },
    ])
  })

  it('never carries a secret value, only its mask', () => {
    const matches = matchSettings(keys, 'linear')
    expect(matches).toHaveLength(1)
    expect(displayValue(matches[0].item)).toBe('••••••••')
  })

  it('caps the group at five rows', () => {
    const many = Array.from({ length: 9 }, (_, i) =>
      key({ key: `HUB_KEY_${i}`, group: 'Hub & web server' }),
    )
    expect(matchSettings(many, 'hub_key')).toHaveLength(5)
  })

  it('lists nothing for a blank query', () => {
    expect(matchSettings(keys, '')).toEqual([])
    expect(matchSettings(keys, '   ')).toEqual([])
  })
})

describe('parseSettingsSearch', () => {
  it('keeps a key name so the filter can be seeded from the url', () => {
    expect(parseSettingsSearch({ q: 'PROVIDER' })).toEqual({ q: 'PROVIDER' })
  })

  // The router hands the component { ...raw, ...parseSettingsSearch(raw) }, so
  // a rejected param has to survive that merge as undefined.
  it('drops a missing, empty or non-string param', () => {
    const landed = (raw: Record<string, unknown>) => ({
      ...raw,
      ...parseSettingsSearch(raw),
    })
    expect(landed({}).q).toBeUndefined()
    expect(landed({ q: '' }).q).toBeUndefined()
    expect(landed({ q: 7 }).q).toBeUndefined()
    expect(landed({ q: true }).q).toBeUndefined()
  })

  it('ignores unrelated params', () => {
    expect(parseSettingsSearch({ issue: 'COD-1341' })).toEqual({})
  })
})

const TRACKER = 'Tracker & issues'

// trackerCatalog mirrors the server's Tracker & issues section: the shared keys,
// the per-provider keys with their catalog tracker tag, and one key from another
// section that the filter must never touch.
function trackerCatalog(provider: string): ConfigKey[] {
  const shared = (k: string, advanced = false) =>
    key({ key: k, group: TRACKER, advanced })
  const owned = (k: string, tracker: string) =>
    key({ key: k, group: TRACKER, advanced: true, tracker })
  return [
    key({
      key: 'TRACKER_PROVIDER',
      group: TRACKER,
      value: provider,
      default: 'linear',
    }),
    shared('LINEAR_TEAM'),
    shared('ISSUE_PREFIX'),
    shared('PROJECT'),
    shared('READY_LABEL'),
    shared('QUARANTINE_LABEL'),
    shared('QUEUED_LABEL'),
    shared('STATUS_TODO', true),
    shared('STATUS_IN_PROGRESS', true),
    shared('STATUS_IN_REVIEW', true),
    shared('STATUS_DONE', true),
    shared('DELIVERED_STATE', true),
    owned('LINEAR_API_KEY', 'linear'),
    owned('LINEAR_BOARD_STATES', 'linear'),
    owned('JIRA_BASE_URL', 'jira'),
    owned('JIRA_EMAIL', 'jira'),
    owned('JIRA_API_TOKEN', 'jira'),
    owned('JIRA_EPIC_TYPE', 'jira'),
    owned('JIRA_BOARD_STATES', 'jira'),
    owned('AZURE_ORG_URL', 'azure'),
    owned('AZURE_PAT', 'azure'),
    owned('AZURE_AREA_PATH', 'azure'),
    owned('AZURE_TEAMS', 'azure'),
    owned('AZURE_BOARD_STATES', 'azure'),
    key({ key: 'BASE_BRANCH', group: 'Git & merge' }),
  ]
}

function trackerSection(provider: string) {
  const keys = trackerCatalog(provider)
  const sections = deriveSections(keys, activeTracker(keys))
  return sections.find((s) => s.group === TRACKER)!
}

describe('activeTracker', () => {
  it('reads the value the dropdown shows', () => {
    expect(activeTracker(trackerCatalog('jira'))).toBe('jira')
  })

  it('falls back to linear on a fresh repo rather than the internal provider', () => {
    expect(
      activeTracker([
        key({ key: 'TRACKER_PROVIDER', group: TRACKER, default: 'linear' }),
      ]),
    ).toBe('linear')
    expect(activeTracker([])).toBe('linear')
  })

  it('normalizes a stray case or spacing in the stored value', () => {
    expect(
      activeTracker([
        key({ key: 'TRACKER_PROVIDER', group: TRACKER, value: ' Azure ' }),
      ]),
    ).toBe('azure')
  })
})

describe('isInactiveTrackerKey', () => {
  it('hides a tagged key belonging to another tracker', () => {
    const jiraKey = key({ key: 'JIRA_EMAIL', group: TRACKER, tracker: 'jira' })
    expect(isInactiveTrackerKey(jiraKey, 'linear')).toBe(true)
    expect(isInactiveTrackerKey(jiraKey, 'jira')).toBe(false)
  })

  it('hides ISSUE_PREFIX only under azure, whose items are addressed by number', () => {
    const prefix = key({ key: 'ISSUE_PREFIX', group: TRACKER })
    expect(isInactiveTrackerKey(prefix, 'azure')).toBe(true)
    expect(isInactiveTrackerKey(prefix, 'jira')).toBe(false)
  })

  it('hides the tracker binding only under internal, which binds to nothing', () => {
    for (const k of ['LINEAR_TEAM', 'PROJECT']) {
      const item = key({ key: k, group: TRACKER })
      expect(isInactiveTrackerKey(item, 'internal')).toBe(true)
      expect(isInactiveTrackerKey(item, 'github')).toBe(false)
    }
  })

  it('never filters a key outside the tracker section', () => {
    const base = key({ key: 'BASE_BRANCH', group: 'Git & merge' })
    for (const tracker of ['linear', 'jira', 'azure', 'github', 'internal']) {
      expect(isInactiveTrackerKey(base, tracker)).toBe(false)
    }
  })
})

describe('deriveSections tracker visibility', () => {
  const alwaysVisible = [
    'TRACKER_PROVIDER',
    'READY_LABEL',
    'QUARANTINE_LABEL',
    'QUEUED_LABEL',
    'STATUS_TODO',
    'STATUS_IN_PROGRESS',
    'STATUS_IN_REVIEW',
    'STATUS_DONE',
    'DELIVERED_STATE',
  ]

  const matrix: Record<string, { shown: string[]; hidden: string[] }> = {
    linear: {
      shown: [
        'LINEAR_API_KEY',
        'LINEAR_BOARD_STATES',
        'LINEAR_TEAM',
        'PROJECT',
        'ISSUE_PREFIX',
      ],
      hidden: [
        'JIRA_BASE_URL',
        'JIRA_BOARD_STATES',
        'AZURE_ORG_URL',
        'AZURE_PAT',
      ],
    },
    jira: {
      shown: [
        'JIRA_BASE_URL',
        'JIRA_EMAIL',
        'JIRA_API_TOKEN',
        'JIRA_EPIC_TYPE',
        'JIRA_BOARD_STATES',
        'LINEAR_TEAM',
        'PROJECT',
        'ISSUE_PREFIX',
      ],
      hidden: [
        'LINEAR_API_KEY',
        'LINEAR_BOARD_STATES',
        'AZURE_ORG_URL',
        'AZURE_BOARD_STATES',
      ],
    },
    azure: {
      shown: [
        'AZURE_ORG_URL',
        'AZURE_PAT',
        'AZURE_AREA_PATH',
        'AZURE_TEAMS',
        'AZURE_BOARD_STATES',
        'LINEAR_TEAM',
        'PROJECT',
      ],
      hidden: [
        'ISSUE_PREFIX',
        'LINEAR_API_KEY',
        'JIRA_BASE_URL',
        'JIRA_BOARD_STATES',
      ],
    },
    github: {
      shown: ['LINEAR_TEAM', 'PROJECT', 'ISSUE_PREFIX'],
      hidden: ['LINEAR_API_KEY', 'JIRA_BASE_URL', 'AZURE_ORG_URL'],
    },
    internal: {
      shown: ['ISSUE_PREFIX'],
      hidden: [
        'LINEAR_TEAM',
        'PROJECT',
        'LINEAR_API_KEY',
        'JIRA_EMAIL',
        'AZURE_PAT',
      ],
    },
  }

  for (const [provider, { shown, hidden }] of Object.entries(matrix)) {
    it(`shows only ${provider}'s own fields`, () => {
      const section = trackerSection(provider)
      const visible = section.keys.map((k) => k.key)
      const rendered = [
        ...section.primaryKeys.map((k) => k.key),
        ...section.advancedKeys.map((k) => k.key),
      ]

      for (const k of [...shown, ...alwaysVisible]) {
        expect(visible).toContain(k)
        expect(rendered).toContain(k)
      }
      for (const k of hidden) {
        expect(visible).not.toContain(k)
        expect(rendered).not.toContain(k)
        expect(section.hiddenKeys.map((h) => h.key)).toContain(k)
      }
    })
  }

  it('shrinks the advanced count by the hidden trackers', () => {
    const all = deriveSections(trackerCatalog('jira'))
    const filtered = trackerSection('jira')
    expect(all.find((s) => s.group === TRACKER)!.advancedKeys.length).toBe(
      filtered.advancedKeys.length + filtered.hiddenKeys.length,
    )
    expect(filtered.hiddenKeys).toHaveLength(7)
  })

  it('leaves the active provider board key for the mapping editor to own', () => {
    expect(trackerSection('jira').advancedKeys.map((k) => k.key)).toContain(
      'JIRA_BOARD_STATES',
    )
    expect(trackerSection('linear').advancedKeys.map((k) => k.key)).toContain(
      'LINEAR_BOARD_STATES',
    )
  })

  it('keeps every key when no tracker is given, so the palette still finds them', () => {
    const section = deriveSections(trackerCatalog('jira')).find(
      (s) => s.group === TRACKER,
    )!
    expect(section.hiddenKeys).toHaveLength(0)
    expect(section.keys.map((k) => k.key)).toContain('AZURE_PAT')
  })

  it('does not filter other sections', () => {
    const git = deriveSections(trackerCatalog('internal'), 'internal').find(
      (s) => s.group === 'Git & merge',
    )!
    expect(git.keys.map((k) => k.key)).toEqual(['BASE_BRANCH'])
    expect(git.hiddenKeys).toHaveLength(0)
  })
})

describe('search reveals hidden tracker keys', () => {
  it('surfaces the linear keys on a jira project and labels them inactive', () => {
    const section = trackerSection('jira')
    const revealed = section.hiddenKeys.filter((k) => matchesQuery(k, 'linear'))
    expect(revealed.map((k) => k.key)).toEqual([
      'LINEAR_API_KEY',
      'LINEAR_BOARD_STATES',
    ])
    expect(inactiveTrackerNote(revealed[0])).toBe('linear — inactive tracker')
  })

  it('leaves an untagged hide without a tracker badge', () => {
    const prefix = trackerSection('azure').hiddenKeys.find(
      (k) => k.key === 'ISSUE_PREFIX',
    )!
    expect(inactiveTrackerNote(prefix)).toBeNull()
  })

  it('reveals nothing when the query matches no hidden key', () => {
    const section = trackerSection('jira')
    expect(section.hiddenKeys.filter((k) => matchesQuery(k, 'ready'))).toEqual(
      [],
    )
  })
})

describe('trackerHint', () => {
  it('explains what LINEAR_TEAM holds under each other tracker', () => {
    expect(trackerHint('LINEAR_TEAM', 'jira')).toBe(
      'holds your Jira project key',
    )
    expect(trackerHint('LINEAR_TEAM', 'azure')).toBe(
      'holds your Azure DevOps project name',
    )
    expect(trackerHint('LINEAR_TEAM', 'github')).toBe('holds your GitHub repo')
    expect(trackerHint('LINEAR_TEAM', 'linear')).toBeNull()
  })

  it('marks PROJECT as the fallback binding for the project-keyed trackers', () => {
    expect(trackerHint('PROJECT', 'jira')).toBe(
      'fallback tracker key when LINEAR_TEAM is unset',
    )
    expect(trackerHint('PROJECT', 'azure')).toBe(
      'fallback tracker key when LINEAR_TEAM is unset',
    )
    expect(trackerHint('PROJECT', 'linear')).toBeNull()
  })

  it('stays silent for keys that are not overloaded', () => {
    expect(trackerHint('READY_LABEL', 'jira')).toBeNull()
  })
})
