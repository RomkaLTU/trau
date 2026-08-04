import { describe, expect, it } from 'vitest'

import type { ConfigKey } from '@/lib/config'
import {
  configFallback,
  draftFor,
  draftIssue,
  filterWorkspaces,
  parseAppURLsValue,
  workspaceLabel,
  workspaceSuggestion,
  workspaceUnrouted,
  type AppURL,
  type Workspace,
} from './app-urls'

function entry(over: Partial<AppURL> = {}): AppURL {
  return {
    id: 1,
    label: 'storefront',
    url: 'http://localhost:3000',
    workspace: '',
    ...over,
  }
}

function key(k: string, value: string): ConfigKey {
  return { key: k, value, layer: 'project', editable: true }
}

describe('draftFor', () => {
  it('seeds an empty draft for a new entry', () => {
    expect(draftFor(null)).toEqual({ label: '', url: '', workspace: '' })
  })

  it('seeds every editable field from the entry', () => {
    expect(draftFor(entry({ workspace: 'web' }))).toEqual({
      label: 'storefront',
      url: 'http://localhost:3000',
      workspace: 'web',
    })
  })
})

describe('workspaceLabel', () => {
  it('names the workspaceless entry as the repo default', () => {
    expect(workspaceLabel('')).toBe('default')
  })

  it('leaves a named workspace alone', () => {
    expect(workspaceLabel('apps/web')).toBe('apps/web')
  })
})

describe('draftIssue', () => {
  it('requires a URL', () => {
    expect(draftIssue({ label: 'web', url: '  ', workspace: '' }, [], null)).toBe(
      'a URL is required',
    )
  })

  it('accepts a draft whose workspace is free', () => {
    const entries = [entry({ id: 1, workspace: 'web' })]
    const draft = { label: '', url: 'http://localhost:3001', workspace: 'api' }
    expect(draftIssue(draft, entries, null)).toBeNull()
  })

  it('refuses a second entry for a workspace already taken', () => {
    const entries = [entry({ id: 1, workspace: 'web' })]
    const draft = { label: '', url: 'http://localhost:3001', workspace: 'web' }
    expect(draftIssue(draft, entries, null)).toContain('workspace')
  })

  it('refuses a second default, naming the fix', () => {
    const entries = [entry({ id: 1, workspace: '' })]
    const draft = { label: '', url: 'http://localhost:3001', workspace: '' }
    expect(draftIssue(draft, entries, null)).toBe(
      'a default app URL already exists for this repo — give this entry a workspace or edit the existing one',
    )
  })

  it('lets an entry keep its own workspace while being edited', () => {
    const entries = [entry({ id: 7, workspace: 'web' })]
    const draft = { label: 'renamed', url: 'http://localhost:3000', workspace: 'web' }
    expect(draftIssue(draft, entries, 7)).toBeNull()
  })

  it('trims before comparing, so a padded workspace still clashes', () => {
    const entries = [entry({ id: 1, workspace: 'web' })]
    const draft = { label: '', url: 'http://localhost:3001', workspace: ' web ' }
    expect(draftIssue(draft, entries, null)).not.toBeNull()
  })
})

const detected: Workspace[] = [
  { name: '@acme/web', path: 'apps/web', dir_name: 'web' },
  { name: '', path: 'apps/api', dir_name: 'api' },
]

describe('workspaceSuggestion', () => {
  it('inserts the manifest name when there is one', () => {
    expect(workspaceSuggestion(detected[0])).toBe('@acme/web')
  })

  it('falls back to the relative path for a nameless manifest', () => {
    expect(workspaceSuggestion(detected[1])).toBe('apps/api')
  })
})

describe('filterWorkspaces', () => {
  it('offers every workspace for an empty field', () => {
    expect(filterWorkspaces(detected, '  ')).toEqual(detected)
  })

  it('matches any of the three forms, case-insensitively', () => {
    expect(filterWorkspaces(detected, 'ACME')).toEqual([detected[0]])
    expect(filterWorkspaces(detected, 'apps/api')).toEqual([detected[1]])
    expect(filterWorkspaces(detected, 'web')).toEqual([detected[0]])
  })

  it('is empty when nothing matches', () => {
    expect(filterWorkspaces(detected, 'mobile')).toEqual([])
  })
})

describe('workspaceUnrouted', () => {
  it('accepts every form the runtime matcher routes', () => {
    for (const name of ['@acme/web', 'apps/web', 'web', 'apps/api', 'api']) {
      expect(workspaceUnrouted(name, detected)).toBe(false)
    }
  })

  it('flags a name matching no detected workspace', () => {
    expect(workspaceUnrouted('mobile', detected)).toBe(true)
  })

  it('stays quiet for the repo default', () => {
    expect(workspaceUnrouted('  ', detected)).toBe(false)
  })

  it('stays quiet when nothing was detected', () => {
    expect(workspaceUnrouted('mobile', [])).toBe(false)
  })
})

describe('parseAppURLsValue', () => {
  it('reads workspace=url pairs and sorts them by workspace', () => {
    expect(
      parseAppURLsValue('web=http://localhost:3000,api=http://localhost:3001'),
    ).toEqual([
      { workspace: 'api', url: 'http://localhost:3001' },
      { workspace: 'web', url: 'http://localhost:3000' },
    ])
  })

  it('keeps the URL intact when it carries its own separators', () => {
    expect(parseAppURLsValue('web=http://localhost:3000/a=b')).toEqual([
      { workspace: 'web', url: 'http://localhost:3000/a=b' },
    ])
  })

  it('drops pairs missing either side', () => {
    expect(parseAppURLsValue('web,=http://x,api=,api=http://y')).toEqual([
      { workspace: 'api', url: 'http://y' },
    ])
  })

  it('yields nothing for an unset key', () => {
    expect(parseAppURLsValue('')).toEqual([])
  })
})

describe('configFallback', () => {
  it('puts APP_URL first as the default target, then the APP_URLS workspaces', () => {
    expect(
      configFallback([
        key('APP_URL', 'http://localhost:3000'),
        key('APP_URLS', 'web=http://localhost:3001'),
        key('BROWSER_VERIFY', 'auto'),
      ]),
    ).toEqual([
      { workspace: '', url: 'http://localhost:3000' },
      { workspace: 'web', url: 'http://localhost:3001' },
    ])
  })

  it('is empty when neither key is set', () => {
    expect(configFallback([key('APP_URL', ''), key('APP_URLS', '')])).toEqual([])
  })

  it('works from APP_URLS alone', () => {
    expect(configFallback([key('APP_URLS', 'api=http://localhost:3001')])).toEqual(
      [{ workspace: 'api', url: 'http://localhost:3001' }],
    )
  })
})
