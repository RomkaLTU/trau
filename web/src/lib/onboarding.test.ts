import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  appURLRowIssue,
  appURLRowsToWrite,
  appURLsCanContinue,
  blankAppURLAccount,
  blankAppURLRow,
  credentialLayer,
  essentialsConfigWrites,
  essentialsProjectKeys,
  joinProject,
  laterStep,
  onboardPath,
  preselectProvider,
  secretPlaceholder,
  trackerCanContinue,
  trackerCanTest,
  trackerConfigValues,
  WIZARD_STEPS,
  writeAppURLRows,
  type AppURLRowFields,
  type RepoInspection,
  type TrackerFields,
} from './onboarding'

function respond(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  } as unknown as Response
}

function inspection(over: Partial<RepoInspection> = {}): RepoInspection {
  return {
    path: '/repo',
    repo_name: 'repo',
    kind: 'repo',
    has_trau_ini: false,
    credentials: [],
    forge: 'github',
    default_branch: 'main',
    findings: [],
    ...over,
  }
}

function fields(over: Partial<TrackerFields> = {}): TrackerFields {
  return {
    linearKey: '',
    jiraBaseUrl: '',
    jiraEmail: '',
    jiraToken: '',
    azureOrgUrl: '',
    azurePat: '',
    binding: '',
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

  it('re-selects azure from a prefill and from credentials alone', () => {
    expect(
      preselectProvider(
        inspection({
          prefill: { provider: 'azure', team: 'Contoso', ready_label: '', epic_flow: false },
        }),
      ),
    ).toBe('azure')
    expect(
      preselectProvider(
        inspection({ credentials: [{ provider: 'azure', layer: 'project' }] }),
      ),
    ).toBe('azure')
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

describe('trackerConfigValues', () => {
  it('always sets TRACKER_PROVIDER and skips empty secrets', () => {
    const keys = trackerConfigValues('linear', fields({ binding: 'COD' }))
    expect(keys).toEqual({ TRACKER_PROVIDER: 'linear', LINEAR_TEAM: 'COD' })
  })

  it('includes a non-empty linear key', () => {
    const keys = trackerConfigValues('linear', fields({ linearKey: 'lin_secret', binding: 'COD' }))
    expect(Object.keys(keys)).toEqual([
      'TRACKER_PROVIDER',
      'LINEAR_API_KEY',
      'LINEAR_TEAM',
    ])
  })

  it('sets only the Jira fields that are filled in', () => {
    const keys = trackerConfigValues(
      'jira',
      fields({ jiraBaseUrl: 'https://acme.atlassian.net', jiraToken: 'tok', binding: 'MELGA' }),
    )
    expect(Object.keys(keys)).toEqual([
      'TRACKER_PROVIDER',
      'JIRA_BASE_URL',
      'JIRA_API_TOKEN',
      'LINEAR_TEAM',
    ])
  })

  it('writes the whole azure set with the team project as LINEAR_TEAM', () => {
    const keys = trackerConfigValues(
      'azure',
      fields({
        azureOrgUrl: '  https://dev.azure.com/contoso  ',
        azurePat: 'pat',
        binding: 'Contoso',
      }),
    )
    expect(keys).toEqual({
      TRACKER_PROVIDER: 'azure',
      AZURE_ORG_URL: 'https://dev.azure.com/contoso',
      AZURE_PAT: 'pat',
      LINEAR_TEAM: 'Contoso',
    })
  })

  it('omits a blank azure PAT so the stored one survives', () => {
    const keys = trackerConfigValues(
      'azure',
      fields({ azureOrgUrl: 'https://dev.azure.com/contoso', binding: 'Contoso' }),
    )
    expect(keys.AZURE_ORG_URL).toBe('https://dev.azure.com/contoso')
    expect(keys).not.toHaveProperty('AZURE_PAT')
  })

  it('sets only the provider for internal', () => {
    const keys = trackerConfigValues('internal', fields())
    expect(keys).toEqual({ TRACKER_PROVIDER: 'internal' })
  })
})

describe('essentialsProjectKeys', () => {
  it('carries the ready label and the epic-flow bool', () => {
    expect(
      essentialsProjectKeys({
        baseBranches: [{ repo: 'acme', root: '/src/acme', branch: 'develop' }],
        forges: [],
        readyLabel: ' ship-it ',
        epicFlow: true,
      }),
    ).toEqual({ READY_LABEL: 'ship-it', EPIC_FLOW: '1' })
  })

  it('leaves a blank label out and encodes epic flow off as 0', () => {
    expect(
      essentialsProjectKeys({
        baseBranches: [{ repo: 'acme', root: '/src/acme', branch: 'develop' }],
        forges: [],
        readyLabel: '  ',
        epicFlow: false,
      }),
    ).toEqual({ EPIC_FLOW: '0' })
  })
})

describe('essentialsConfigWrites', () => {
  it('writes base branch, ready label, and the epic-flow bool for a lone repo', () => {
    const writes = essentialsConfigWrites(
      {
        baseBranches: [{ repo: 'acme', root: '/src/acme', branch: 'develop' }],
        forges: [{ root: '/src/acme', forge: '' }],
        readyLabel: 'ready-for-agent',
        epicFlow: true,
      },
      null,
    )
    expect(writes).toEqual([
      { root: '/src/acme', key: 'BASE_BRANCH', value: 'develop', layer: 'project' },
      { root: '/src/acme', key: 'READY_LABEL', value: 'ready-for-agent', layer: 'project' },
      { root: '/src/acme', key: 'EPIC_FLOW', value: '1', layer: 'project' },
    ])
  })

  it('leaves the project-wide keys to the project when there is one', () => {
    const writes = essentialsConfigWrites(
      {
        baseBranches: [
          { repo: 'api', root: '/src/api', branch: 'main' },
          { repo: 'web', root: '/src/web', branch: ' master ' },
        ],
        forges: [],
        readyLabel: 'ready-for-agent',
        epicFlow: false,
      },
      'platform',
    )
    expect(writes).toEqual([
      { root: '/src/api', key: 'BASE_BRANCH', value: 'main', layer: 'project' },
      { root: '/src/web', key: 'BASE_BRANCH', value: 'master', layer: 'project' },
    ])
  })

  it('addresses members sharing a repo name by their own root', () => {
    const writes = essentialsConfigWrites(
      {
        baseBranches: [
          { repo: 'svc', root: '/src/dupA/svc', branch: 'main' },
          { repo: 'svc', root: '/src/dupB/svc', branch: 'develop' },
        ],
        forges: [],
        readyLabel: 'ready-for-agent',
        epicFlow: false,
      },
      'dupes',
    )
    expect(writes).toEqual([
      { root: '/src/dupA/svc', key: 'BASE_BRANCH', value: 'main', layer: 'project' },
      { root: '/src/dupB/svc', key: 'BASE_BRANCH', value: 'develop', layer: 'project' },
    ])
  })

  it('encodes epic flow off as 0 and skips only the members left blank', () => {
    const writes = essentialsConfigWrites(
      {
        baseBranches: [
          { repo: 'api', root: '/src/api', branch: '  ' },
          { repo: 'web', root: '/src/web', branch: 'master' },
        ],
        forges: [],
        readyLabel: 'ready-for-agent',
        epicFlow: false,
      },
      null,
    )
    expect(writes.filter((w) => w.key === 'BASE_BRANCH')).toEqual([
      { root: '/src/web', key: 'BASE_BRANCH', value: 'master', layer: 'project' },
    ])
    expect(writes.find((w) => w.key === 'EPIC_FLOW')?.value).toBe('0')
  })

  it('writes each member its own forge alongside its base branch', () => {
    const writes = essentialsConfigWrites(
      {
        baseBranches: [
          { repo: 'api', root: '/src/api', branch: 'main' },
          { repo: 'web', root: '/src/web', branch: 'master' },
        ],
        forges: [
          { root: '/src/api', forge: 'github' },
          { root: '/src/web', forge: 'azure' },
        ],
        readyLabel: 'ready-for-agent',
        epicFlow: false,
      },
      'platform',
    )
    expect(writes).toEqual([
      { root: '/src/api', key: 'BASE_BRANCH', value: 'main', layer: 'project' },
      { root: '/src/web', key: 'BASE_BRANCH', value: 'master', layer: 'project' },
      { root: '/src/api', key: 'FORGE', value: 'github', layer: 'project' },
      { root: '/src/web', key: 'FORGE', value: 'azure', layer: 'project' },
    ])
  })

  it('skips the members whose forge was left to the remote', () => {
    const writes = essentialsConfigWrites(
      {
        baseBranches: [
          { repo: 'api', root: '/src/api', branch: 'main' },
          { repo: 'web', root: '/src/web', branch: 'master' },
        ],
        forges: [
          { root: '/src/api', forge: '' },
          { root: '/src/web', forge: 'gitlab' },
        ],
        readyLabel: 'ready-for-agent',
        epicFlow: false,
      },
      'platform',
    )
    expect(writes.filter((w) => w.key === 'FORGE')).toEqual([
      { root: '/src/web', key: 'FORGE', value: 'gitlab', layer: 'project' },
    ])
  })
})

describe('trackerCanContinue', () => {
  it('lets internal through with no binding or test', () => {
    expect(trackerCanContinue('internal', fields(), 'idle')).toBe(true)
  })

  it('blocks an external provider until a binding is chosen and the test passes', () => {
    expect(trackerCanContinue('linear', fields(), 'ok')).toBe(false)
    expect(trackerCanContinue('linear', fields({ binding: 'COD' }), 'idle')).toBe(false)
    expect(trackerCanContinue('linear', fields({ binding: 'COD' }), 'fail')).toBe(false)
    expect(trackerCanContinue('linear', fields({ binding: 'COD' }), 'ok')).toBe(true)
  })

  it('needs nothing beyond the team project for azure — its work items carry no prefix', () => {
    expect(trackerCanContinue('azure', fields({ binding: 'Contoso' }), 'ok')).toBe(true)
    expect(trackerCanContinue('azure', fields({ binding: 'Contoso' }), 'idle')).toBe(false)
    expect(trackerCanContinue('azure', fields(), 'ok')).toBe(false)
  })

  it('blocks when no provider is chosen', () => {
    expect(trackerCanContinue(null, fields({ binding: 'COD' }), 'ok')).toBe(false)
  })
})

describe('trackerCanTest', () => {
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

  it('blocks azure until both the organization URL and the PAT are filled', () => {
    const org = 'https://dev.azure.com/contoso'
    expect(trackerCanTest('azure', fields({ azureOrgUrl: org }), false)).toBe(false)
    expect(trackerCanTest('azure', fields({ azureOrgUrl: org, azurePat: 'pat' }), false)).toBe(true)
  })

  it('allows blank fields when stored credentials exist to fall back to', () => {
    expect(trackerCanTest('jira', fields(), true)).toBe(true)
    expect(trackerCanTest('linear', fields(), true)).toBe(true)
    expect(trackerCanTest('azure', fields(), true)).toBe(true)
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

describe('onboardPath', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('inspects a path, registers it, and reports the project backing it', async () => {
    const seen: string[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) => {
        seen.push(url)
        if (url.endsWith('/inspect')) return respond(200, inspection({ path: '/Projects/acme' }))
        if (url.endsWith('/projects')) {
          return respond(200, { projects: [{ id: 'acme', name: 'acme', repos: ['/Projects/acme'] }] })
        }
        return respond(201, { name: 'acme', root: '/Projects/acme' })
      }),
    )

    const member = await onboardPath('/Projects/acme')

    expect(seen).toEqual(['/api/v1/repos/inspect', '/api/v1/repos', '/api/v1/projects'])
    expect(member.repo).toBe('acme')
    expect(member.root).toBe('/Projects/acme')
    expect(member.project).toBe('acme')
  })
})

describe('joinProject', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  function collect(): [string, unknown][] {
    const calls: [string, unknown][] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init?: RequestInit) => {
        calls.push([url, init?.body ? JSON.parse(String(init.body)) : null])
        return respond(200, { id: 'p1', name: 'acme', repos: [] })
      }),
    )
    return calls
  }

  it('creates the project once and adds every member root', async () => {
    const calls = collect()

    expect(await joinProject(null, 'acme', ['/Projects/api', '/Projects/web'])).toBe('p1')
    expect(calls).toEqual([
      ['/api/v1/projects', { name: 'acme' }],
      ['/api/v1/projects/p1/repos', { repo: '/Projects/api' }],
      ['/api/v1/projects/p1/repos', { repo: '/Projects/web' }],
    ])
  })

  it('adds to an existing project instead of creating another', async () => {
    const calls = collect()

    expect(await joinProject('p1', 'acme', ['/elsewhere/ops'])).toBe('p1')
    expect(calls).toEqual([['/api/v1/projects/p1/repos', { repo: '/elsewhere/ops' }]])
  })
})

describe('WIZARD_STEPS', () => {
  it('offers app urls between the essentials and the seed sync', () => {
    expect(WIZARD_STEPS.map((s) => s.id)).toEqual([
      'path',
      'detect',
      'tracker',
      'essentials',
      'appurls',
      'sync',
      'done',
    ])
  })
})

describe('appURLRowIssue', () => {
  function row(over: Partial<AppURLRowFields> = {}): AppURLRowFields {
    return { ...blankAppURLRow(), ...over }
  }

  it('leaves an untouched row alone so the step can be skipped', () => {
    expect(appURLRowIssue([blankAppURLRow()], 0)).toBeNull()
    expect(appURLsCanContinue([blankAppURLRow()])).toBe(true)
  })

  it('accepts a single URL with nothing else filled in', () => {
    expect(appURLRowIssue([row({ url: 'http://localhost:3000' })], 0)).toBeNull()
  })

  it('asks for a URL once anything else on the row is filled in', () => {
    expect(appURLRowIssue([row({ label: 'storefront' })], 0)).toBe('a URL is required')
    expect(
      appURLRowIssue([row({ accounts: [{ ...blankAppURLAccount(), label: 'admin' }] })], 0),
    ).toBe('a URL is required')
  })

  it('reports a repeated workspace on the later row only', () => {
    const rows = [
      row({ url: 'http://localhost:3000', workspace: 'web' }),
      row({ url: 'http://localhost:4000', workspace: 'web' }),
    ]
    expect(appURLRowIssue(rows, 0)).toBeNull()
    expect(appURLRowIssue(rows, 1)).toContain('already listed above')
    expect(appURLsCanContinue(rows)).toBe(false)
  })

  it('reports a second default entry as a missing workspace', () => {
    const rows = [row({ url: 'http://localhost:3000' }), row({ url: 'http://localhost:4000' })]
    expect(appURLRowIssue(rows, 1)).toBe(
      'a default app URL is already listed above — give this entry a workspace',
    )
  })

  it('requires a label on a QA account that was typed into', () => {
    const rows = [
      row({
        url: 'http://localhost:3000',
        accounts: [{ ...blankAppURLAccount(), username: 'qa@example.test' }],
      }),
    ]
    expect(appURLRowIssue(rows, 0)).toBe('every QA account needs a label')
  })

  it('ignores a QA account row nobody typed into', () => {
    const rows = [row({ url: 'http://localhost:3000', accounts: [blankAppURLAccount()] })]
    expect(appURLRowIssue(rows, 0)).toBeNull()
  })
})

describe('appURLRowsToWrite', () => {
  it('keeps only the rows carrying a URL', () => {
    const filled = { ...blankAppURLRow(), url: 'http://localhost:3000' }
    expect(appURLRowsToWrite([filled, blankAppURLRow()])).toEqual([filled])
  })
})

describe('writeAppURLRows', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  function collect(): [string, unknown][] {
    const calls: [string, unknown][] = []
    let id = 0
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init?: RequestInit) => {
        calls.push([url, init?.body ? JSON.parse(String(init.body)) : null])
        id += 1
        return respond(201, { id, label: '', url: '', workspace: '' })
      }),
    )
    return calls
  }

  it('creates each filled URL and attaches the accounts entered under it', async () => {
    const calls = collect()

    await writeAppURLRows('acme', [
      {
        url: ' http://localhost:3000 ',
        label: 'storefront',
        workspace: '',
        accounts: [blankAppURLAccount()],
      },
      {
        url: 'http://localhost:4000',
        label: '',
        workspace: ' api ',
        accounts: [
          { label: 'admin', username: 'qa@example.test', secret: 'pw', description: 'checkout' },
        ],
      },
      blankAppURLRow(),
    ])

    expect(calls).toEqual([
      [
        '/api/v1/repos/acme/app-urls',
        { label: 'storefront', url: 'http://localhost:3000', workspace: '' },
      ],
      [
        '/api/v1/repos/acme/app-urls',
        { label: '', url: 'http://localhost:4000', workspace: 'api' },
      ],
      [
        '/api/v1/repos/acme/qa/accounts',
        {
          label: 'admin',
          username: 'qa@example.test',
          secret: 'pw',
          description: 'checkout',
          app_url_id: 2,
        },
      ],
    ])
  })

  it('writes nothing when every row was left untouched', async () => {
    const calls = collect()

    await writeAppURLRows('acme', [blankAppURLRow(), blankAppURLRow()])

    expect(calls).toEqual([])
  })
})
