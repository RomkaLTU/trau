import { describe, expect, it } from 'vitest'

import type { ConfigKey } from '@/lib/config'
import {
  configFallback,
  draftFor,
  draftIssue,
  parseAppURLsValue,
  workspaceLabel,
  type AppURL,
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
