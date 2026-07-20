import { describe, expect, it } from 'vitest'

import {
  credentialLayer,
  essentialsConfigWrites,
  laterStep,
  preselectProvider,
  secretPlaceholder,
  trackerCanContinue,
  trackerCanTest,
  trackerConfigWrites,
  type RepoInspection,
  type TrackerFields,
} from './onboarding'

function inspection(over: Partial<RepoInspection> = {}): RepoInspection {
  return {
    path: '/repo',
    repo_name: 'repo',
    has_trau_ini: false,
    credentials: [],
    default_branch: 'main',
    findings: [],
    ...over,
  }
}

describe('preselectProvider', () => {
  it('prefers the detected provider', () => {
    expect(preselectProvider(inspection({ detected_provider: 'jira' }))).toBe('jira')
  })

  it('prefers the prefill provider over the detected one', () => {
    expect(
      preselectProvider(
        inspection({
          detected_provider: 'linear',
          prefill: { provider: 'jira', team: '', ready_label: '', epic_flow: false },
        }),
      ),
    ).toBe('jira')
  })

  it('falls back to the provider whose credentials were found', () => {
    expect(
      preselectProvider(
        inspection({ credentials: [{ provider: 'jira', layer: 'project' }] }),
      ),
    ).toBe('jira')
  })

  it('is null when nothing is detected and no credentials exist', () => {
    expect(preselectProvider(inspection())).toBeNull()
  })
})

describe('credentialLayer', () => {
  it('reports the layer a provider is already stored in', () => {
    const insp = inspection({ credentials: [{ provider: 'jira', layer: 'project' }] })
    expect(credentialLayer(insp, 'jira')).toBe('project')
  })

  it('is null when the provider has no stored credentials', () => {
    const insp = inspection({ credentials: [{ provider: 'linear', layer: 'none' }] })
    expect(credentialLayer(insp, 'linear')).toBeNull()
  })
})

describe('trackerConfigWrites', () => {
  it('always writes TRACKER_PROVIDER and skips empty secrets', () => {
    const writes = trackerConfigWrites('linear', {
      linearKey: '',
      jiraBaseUrl: '',
      jiraEmail: '',
      jiraToken: '',
      binding: 'COD',
    })
    expect(writes).toEqual([
      { key: 'TRACKER_PROVIDER', value: 'linear', layer: 'project' },
      { key: 'LINEAR_TEAM', value: 'COD', layer: 'project' },
    ])
  })

  it('includes a non-empty linear key', () => {
    const writes = trackerConfigWrites('linear', {
      linearKey: 'lin_secret',
      jiraBaseUrl: '',
      jiraEmail: '',
      jiraToken: '',
      binding: 'COD',
    })
    expect(writes.map((w) => w.key)).toEqual([
      'TRACKER_PROVIDER',
      'LINEAR_API_KEY',
      'LINEAR_TEAM',
    ])
  })

  it('writes only the Jira fields that are filled in', () => {
    const writes = trackerConfigWrites('jira', {
      linearKey: '',
      jiraBaseUrl: 'https://acme.atlassian.net',
      jiraEmail: '',
      jiraToken: 'tok',
      binding: 'MELGA',
    })
    expect(writes.map((w) => w.key)).toEqual([
      'TRACKER_PROVIDER',
      'JIRA_BASE_URL',
      'JIRA_API_TOKEN',
      'LINEAR_TEAM',
    ])
  })

  it('writes only the provider for internal', () => {
    const writes = trackerConfigWrites('internal', {
      linearKey: '',
      jiraBaseUrl: '',
      jiraEmail: '',
      jiraToken: '',
      binding: '',
    })
    expect(writes).toEqual([
      { key: 'TRACKER_PROVIDER', value: 'internal', layer: 'project' },
    ])
  })
})

describe('essentialsConfigWrites', () => {
  it('writes base branch, ready label, and the epic-flow bool', () => {
    const writes = essentialsConfigWrites({
      baseBranch: 'develop',
      readyLabel: 'ready-for-agent',
      epicFlow: true,
    })
    expect(writes).toEqual([
      { key: 'BASE_BRANCH', value: 'develop', layer: 'project' },
      { key: 'READY_LABEL', value: 'ready-for-agent', layer: 'project' },
      { key: 'EPIC_FLOW', value: '1', layer: 'project' },
    ])
  })

  it('encodes epic flow off as 0 and skips a blank branch', () => {
    const writes = essentialsConfigWrites({
      baseBranch: '  ',
      readyLabel: 'ready-for-agent',
      epicFlow: false,
    })
    expect(writes.find((w) => w.key === 'BASE_BRANCH')).toBeUndefined()
    expect(writes.find((w) => w.key === 'EPIC_FLOW')?.value).toBe('0')
  })
})

describe('trackerCanContinue', () => {
  it('lets internal through with no binding or test', () => {
    expect(trackerCanContinue('internal', '', 'idle')).toBe(true)
  })

  it('blocks an external provider until a binding is chosen and the test passes', () => {
    expect(trackerCanContinue('linear', '', 'ok')).toBe(false)
    expect(trackerCanContinue('linear', 'COD', 'idle')).toBe(false)
    expect(trackerCanContinue('linear', 'COD', 'fail')).toBe(false)
    expect(trackerCanContinue('linear', 'COD', 'ok')).toBe(true)
  })

  it('blocks when no provider is chosen', () => {
    expect(trackerCanContinue(null, 'COD', 'ok')).toBe(false)
  })
})

describe('trackerCanTest', () => {
  function fields(over: Partial<TrackerFields> = {}): TrackerFields {
    return { linearKey: '', jiraBaseUrl: '', jiraEmail: '', jiraToken: '', binding: '', ...over }
  }

  it('blocks jira until the site URL, email, and token are all filled', () => {
    expect(
      trackerCanTest('jira', fields({ jiraEmail: 'e@x.com', jiraToken: 'tok' }), false),
    ).toBe(false)
    expect(
      trackerCanTest(
        'jira',
        fields({ jiraBaseUrl: 'https://acme.atlassian.net', jiraEmail: 'e@x.com', jiraToken: 'tok' }),
        false,
      ),
    ).toBe(true)
  })

  it('blocks linear until the API key is filled', () => {
    expect(trackerCanTest('linear', fields(), false)).toBe(false)
    expect(trackerCanTest('linear', fields({ linearKey: 'lin_key' }), false)).toBe(true)
  })

  it('allows blank fields when stored credentials exist to fall back to', () => {
    expect(trackerCanTest('jira', fields(), true)).toBe(true)
    expect(trackerCanTest('linear', fields(), true)).toBe(true)
  })

  it('never blocks internal', () => {
    expect(trackerCanTest('internal', fields(), false)).toBe(true)
  })
})

describe('secretPlaceholder', () => {
  it('shows the replace hint when a secret is already stored', () => {
    expect(secretPlaceholder(true, 'lin_...')).toContain('enter to replace')
  })

  it('shows the fallback placeholder when nothing is stored', () => {
    expect(secretPlaceholder(false, 'lin_...')).toBe('lin_...')
  })
})

describe('laterStep', () => {
  it('keeps the furthest-reached step monotonic', () => {
    expect(laterStep('tracker', 'detect')).toBe('tracker')
    expect(laterStep('path', 'sync')).toBe('sync')
  })
})
