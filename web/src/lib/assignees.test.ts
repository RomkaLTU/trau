import { afterEach, describe, expect, it, vi } from 'vitest'

import { type Assignee } from './assignee'
import {
  AssignError,
  assignIssue,
  assignableUsersQueryOptions,
  isAssignUnsupported,
  publishAssignment,
} from './assignees'
import { type Issue } from './issues'

afterEach(() => {
  vi.unstubAllGlobals()
})

function jsonResponse(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  } as Response
}

const ada: Assignee = { id: 'usr_1', name: 'Ada Lovelace', me: false }

const issue = { repo: 'acme', id: 'COD-1', assignee: ada } as Issue

function lookupError(query: string): Promise<unknown> {
  return Promise.resolve(
    assignableUsersQueryOptions('acme', query).queryFn?.({} as never),
  ).catch((err: unknown) => err)
}

describe('assignableUsersQueryOptions', () => {
  it('asks the tracker for the narrowing query and keeps it briefly', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { users: [ada] }))
    vi.stubGlobal('fetch', fetchMock)

    const options = assignableUsersQueryOptions('acme/api', 'ada')
    expect(options.queryKey).toEqual(['assignable-users', 'acme/api', 'ada'])
    expect(options.staleTime).toBe(60_000)

    await expect(options.queryFn?.({} as never)).resolves.toEqual({ users: [ada] })
    expect((fetchMock.mock.calls[0] as [string])[0]).toBe(
      '/api/v1/repos/acme%2Fapi/assignable-users?query=ada',
    )
  })

  it('types a tracker with no assignment API as unsupported, anything else as a failure', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse(409, {
          error: "this repo's tracker does not support assignment",
        }),
      ),
    )
    const unsupported = await lookupError('')
    expect(isAssignUnsupported(unsupported)).toBe(true)
    expect((unsupported as AssignError).message).toBe(
      "this repo's tracker does not support assignment",
    )

    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(jsonResponse(502, { error: 'tracker unreachable' })),
    )
    const failed = await lookupError('')
    expect(isAssignUnsupported(failed)).toBe(false)
    expect((failed as AssignError).message).toBe('tracker unreachable')
  })
})

describe('assignIssue', () => {
  it('puts the chosen assignee to the issue', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, issue))
    vi.stubGlobal('fetch', fetchMock)

    await expect(assignIssue('acme', 'COD-1', ada)).resolves.toEqual(issue)

    const [input, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(input).toBe('/api/v1/repos/acme/issues/COD-1/assignee')
    expect(init.method).toBe('PUT')
    expect(JSON.parse(init.body as string)).toEqual({
      id: 'usr_1',
      name: 'Ada Lovelace',
    })
  })

  it('unassigns with an empty id', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, issue))
    vi.stubGlobal('fetch', fetchMock)

    await assignIssue('acme', 'COD-1', null)

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(JSON.parse(init.body as string)).toEqual({ id: '', name: '' })
  })

  it('carries the refusal message the tracker answered with', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse(502, { error: 'assign issue: not a member of the team' }),
      ),
    )
    await expect(assignIssue('acme', 'COD-1', ada)).rejects.toThrow(
      'assign issue: not a member of the team',
    )
  })
})

describe('publishAssignment', () => {
  it('writes the assigned issue in place and refreshes the board and facet counts', () => {
    const setQueryData = vi.fn()
    const invalidateQueries = vi.fn()
    publishAssignment(
      { setQueryData, invalidateQueries } as never,
      'acme',
      issue,
    )

    expect(setQueryData).toHaveBeenCalledWith(['issue', 'acme', 'COD-1'], issue)
    expect(invalidateQueries.mock.calls.map(([arg]) => arg)).toEqual([
      { queryKey: ['backlog', 'acme'] },
      { queryKey: ['assignees', 'acme'] },
    ])
  })
})
