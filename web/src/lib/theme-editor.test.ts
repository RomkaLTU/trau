import { describe, expect, it } from 'vitest'

import {
  CORE_ROLES,
  addMode,
  asThemeDocument,
  draftFromDocument,
  draftIssues,
  draftModes,
  exportDocument,
  hasIssues,
  isThemeColor,
  pinCounts,
  previewDocument,
  removeMode,
  seedRoles,
  setRole,
  slugify,
  themeJSON,
  type ThemeDocument,
} from '@/lib/theme-editor'
import type { ThemeSummary } from '@/lib/palette'

function roles(prefix: string): Record<string, string> {
  return Object.fromEntries(
    CORE_ROLES.map((role, i) => [role, `#${prefix}${prefix}${i.toString(16)}0`]),
  )
}

function document(overrides: Partial<ThemeDocument> = {}): ThemeDocument {
  return {
    name: 'Nord',
    slug: 'nord',
    modes: { light: { roles: roles('aa') }, dark: { roles: roles('11') } },
    ...overrides,
  }
}

describe('slugify', () => {
  it.each([
    ['My Theme', 'my-theme'],
    ['  Solar Flare 2  ', 'solar-flare-2'],
    ['Ümlaut', 'mlaut'],
    ['---', ''],
    ['', ''],
    ['!!!', ''],
  ])('derives %j into %j', (name, want) => {
    expect(slugify(name)).toBe(want)
  })
})

describe('isThemeColor', () => {
  it.each(['#fff', '#7d56f4', '#7D56F4', '#7d56f480'])('accepts %s', (value) => {
    expect(isThemeColor(value)).toBe(true)
  })

  it.each(['7d56f4', 'rebeccapurple', '#7d56f', 'var(--brand)', ''])(
    'rejects %j',
    (value) => {
      expect(isThemeColor(value)).toBe(false)
    },
  )
})

describe('draftFromDocument', () => {
  it('keeps the vars and tui blocks a duplicated theme pinned', () => {
    const source = document({
      modes: {
        light: {
          roles: roles('aa'),
          vars: { '--ring': '#0f0f0f' },
          tui: { brand: '#010203' },
        },
      },
    })
    const draft = draftFromDocument(source, 'Nord copy')
    expect(draft.name).toBe('Nord copy')
    expect(draft.source).toBe('nord')

    const exported = exportDocument(draft)
    expect(exported.slug).toBe('nord-copy')
    expect(exported.modes.light?.vars).toEqual({ '--ring': '#0f0f0f' })
    expect(exported.modes.light?.tui).toEqual({ brand: '#010203' })
  })

  it('counts the pins a duplicate brought, and drops them on request', () => {
    const source = document({
      modes: {
        light: {
          roles: roles('aa'),
          vars: { '--ring': '#0f0f0f', '--cli': '#0f0f0f' },
          tui: { brand: '#010203' },
        },
        dark: { roles: roles('11') },
      },
    })
    const draft = draftFromDocument(source, 'Nord copy')
    expect(pinCounts(draft)).toEqual({ vars: 2, tui: 1 })

    const dropped = exportDocument(draft, true)
    expect(dropped.modes.light?.vars).toBeUndefined()
    expect(dropped.modes.light?.tui).toBeUndefined()
    expect(Object.keys(dropped.modes.light!.roles)).toHaveLength(
      CORE_ROLES.length,
    )
  })

  it('carries author and version through the round trip', () => {
    const draft = draftFromDocument(document({ author: 'A', version: '2' }))
    const exported = exportDocument(draft)
    expect(exported.author).toBe('A')
    expect(exported.version).toBe('2')
    expect(Object.keys(exported.modes)).toEqual(['light', 'dark'])
  })
})

describe('previewDocument', () => {
  it('substitutes the last valid color for a role mid-edit', () => {
    let draft = draftFromDocument(document(), 'My Theme')
    draft = setRole(draft, 'light', 'brand', '#123456')
    draft = setRole(draft, 'light', 'brand', '#12')

    expect(previewDocument(draft).modes.light?.roles.brand).toBe('#123456')
    expect(exportDocument(draft).modes.light?.roles.brand).toBe('#12')
  })

  it('gives an unnamed draft a slug the resolver accepts', () => {
    const draft = draftFromDocument(document(), '')
    expect(previewDocument(draft).slug).toBe('draft')
    expect(previewDocument(draft).name).toBe('draft')
  })
})

describe('draftIssues', () => {
  it('passes a complete draft', () => {
    const draft = draftFromDocument(document(), 'My Theme')
    const issues = draftIssues(draft, ['default', 'nord'])
    expect(hasIssues(issues)).toBe(false)
  })

  it('names an unnamed draft', () => {
    const issues = draftIssues(draftFromDocument(document(), '  '), [])
    expect(issues.name).toBe('name this theme')
    expect(hasIssues(issues)).toBe(true)
  })

  it('refuses a predefined slug', () => {
    const issues = draftIssues(draftFromDocument(document(), 'Nord'), ['nord'])
    expect(issues.slug).toContain('nord')
    expect(hasIssues(issues)).toBe(true)
  })

  it('reports the mode and role an invalid hex sits in', () => {
    let draft = draftFromDocument(document(), 'My Theme')
    draft = setRole(draft, 'dark', 'surface', 'rebeccapurple')
    const issues = draftIssues(draft, [])
    expect(issues.roles.dark?.surface).toContain('hex color')
    expect(issues.roles.light).toBeUndefined()
  })
})

describe('modes', () => {
  it('adds a mode from a seed and drops it again', () => {
    const light = document({ modes: { light: { roles: roles('aa') } } })
    let draft = draftFromDocument(light, 'My Theme')
    expect(draftModes(draft)).toEqual(['light'])

    draft = addMode(draft, 'dark', roles('11'))
    expect(draftModes(draft)).toEqual(['light', 'dark'])
    expect(draft.modes.dark?.roles.brand).toBe('#111100')

    draft = removeMode(draft, 'dark')
    expect(draftModes(draft)).toEqual(['light'])
  })

  it('keeps the last mode, since a theme with none paints nothing', () => {
    const light = document({ modes: { light: { roles: roles('aa') } } })
    const draft = draftFromDocument(light, 'My Theme')
    expect(draftModes(removeMode(draft, 'light'))).toEqual(['light'])
  })

  it('seeds a new mode from the same polarity of the source theme', () => {
    const summaries: ThemeSummary[] = [
      {
        slug: 'nord',
        name: 'Nord',
        modes: ['light', 'dark'],
        roles: { light: roles('aa'), dark: roles('11') },
        origin: 'bundled',
      },
    ]
    const light = document({ modes: { light: { roles: roles('aa') } } })
    const draft = draftFromDocument(light, 'My Theme')
    expect(seedRoles(draft, 'dark', summaries).brand).toBe('#111100')
  })
})

describe('themeJSON', () => {
  it('renders the file the hub writes, and parses back to the same document', () => {
    const draft = draftFromDocument(document(), 'My Theme')
    const text = themeJSON(exportDocument(draft))
    expect(text.endsWith('\n')).toBe(true)
    expect(asThemeDocument(JSON.parse(text))).toEqual(exportDocument(draft))
  })
})

describe('asThemeDocument', () => {
  it.each([null, 'nope', {}, { name: 'T', slug: 't' }, { name: 'T', slug: 't', modes: { light: {} } }])(
    'rejects %j',
    (input) => {
      expect(asThemeDocument(input)).toBeNull()
    },
  )

  it('accepts a document the hub served', () => {
    expect(asThemeDocument(document())).not.toBeNull()
  })
})
