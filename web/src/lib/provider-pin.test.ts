import { afterEach, describe, expect, it, vi } from 'vitest'

import type { Issue } from './issues'
import {
  clearedProviderPin,
  pinProvider,
  providerPinLabel,
  publishProviderPin,
  resolveProviderPin,
} from './provider-pin'

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

const issue = { repo: 'acme', id: 'COD-1', provider_pin: 'codex' } as Issue

describe('pinProvider', () => {
  it('puts the chosen provider to the issue', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, issue))
    vi.stubGlobal('fetch', fetchMock)

    await expect(pinProvider('acme', 'COD-1', 'codex')).resolves.toEqual(issue)

    const [input, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(input).toBe('/api/v1/repos/acme/issues/COD-1/provider')
    expect(init.method).toBe('PUT')
    expect(JSON.parse(init.body as string)).toEqual({ provider: 'codex' })
  })

  it('clears the pin with an empty provider', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, issue))
    vi.stubGlobal('fetch', fetchMock)

    await pinProvider('acme', 'COD-1', '')

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(JSON.parse(init.body as string)).toEqual({ provider: '' })
  })

  it('carries the refusal message the hub answered with', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse(400, {
          error: 'unknown provider "gpt" (expected: claude | codex | kimi)',
        }),
      ),
    )
    await expect(pinProvider('acme', 'COD-1', 'gpt')).rejects.toThrow(
      'unknown provider "gpt" (expected: claude | codex | kimi)',
    )
  })
})

describe('publishProviderPin', () => {
  it('writes the pinned issue in place and refreshes the board and queue tags', () => {
    const setQueryData = vi.fn()
    const invalidateQueries = vi.fn()

    publishProviderPin({ setQueryData, invalidateQueries } as never, 'acme', issue)

    expect(setQueryData).toHaveBeenCalledWith(['issue', 'acme', 'COD-1'], issue)
    expect(invalidateQueries.mock.calls.map(([arg]) => arg)).toEqual([
      { queryKey: ['backlog', 'acme'] },
      { queryKey: ['queue', 'acme'] },
    ])
  })
})

describe('resolveProviderPin', () => {
  it('shows the epic it inherits from when the slice pins nothing', () => {
    const pin = resolveProviderPin({
      provider_inherited: 'codex',
      provider_inherited_from: 'COD-123',
    })
    expect(pin).toEqual({ kind: 'inherited', provider: 'codex', from: 'COD-123' })
    expect(providerPinLabel(pin)).toBe('Inherited from COD-123 (codex)')
  })

  it("prefers the slice's own pin over the inherited one", () => {
    const pin = resolveProviderPin({
      provider_pin: 'claude',
      provider_inherited: 'codex',
      provider_inherited_from: 'COD-123',
    })
    expect(pin).toEqual({ kind: 'pinned', provider: 'claude' })
    expect(providerPinLabel(pin)).toBe('claude')
  })

  it('falls back to the repo default with no pin anywhere', () => {
    expect(providerPinLabel(resolveProviderPin({}))).toBe('Repo default')
  })
})

describe('clearedProviderPin', () => {
  it('returns a pinned slice to the inherited state, not the repo default', () => {
    expect(
      providerPinLabel(
        clearedProviderPin({
          provider_pin: 'claude',
          provider_inherited: 'codex',
          provider_inherited_from: 'COD-123',
        }),
      ),
    ).toBe('Inherited from COD-123 (codex)')
  })

  it('returns to the repo default when there is nothing to inherit', () => {
    expect(providerPinLabel(clearedProviderPin({ provider_pin: 'claude' }))).toBe(
      'Repo default',
    )
  })
})
