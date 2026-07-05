import { afterEach, describe, expect, it, vi } from 'vitest'

import { apiFetch, UnauthorizedError } from './api'
import { clearServeToken, onUnauthorized, setServeToken } from './auth'

function headersOf(mock: ReturnType<typeof vi.fn>): Headers {
  return new Headers((mock.mock.calls[0][1] as RequestInit).headers)
}

afterEach(() => {
  clearServeToken()
  vi.unstubAllGlobals()
})

describe('apiFetch', () => {
  it('attaches the stored token as a bearer credential', async () => {
    setServeToken('sekret')
    const fetchMock = vi.fn().mockResolvedValue({ status: 200 } as Response)
    vi.stubGlobal('fetch', fetchMock)

    await apiFetch('/api/v1/health')

    expect(headersOf(fetchMock).get('Authorization')).toBe('Bearer sekret')
  })

  it('sends no Authorization header when tokenless', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ status: 200 } as Response)
    vi.stubGlobal('fetch', fetchMock)

    await apiFetch('/api/v1/health')

    expect(headersOf(fetchMock).has('Authorization')).toBe(false)
  })

  it('preserves caller headers alongside the token', async () => {
    setServeToken('tok')
    const fetchMock = vi.fn().mockResolvedValue({ status: 200 } as Response)
    vi.stubGlobal('fetch', fetchMock)

    await apiFetch('/api/v1/repos/x/config', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
    })

    const headers = headersOf(fetchMock)
    expect(headers.get('Authorization')).toBe('Bearer tok')
    expect(headers.get('Content-Type')).toBe('application/json')
  })

  it('raises the unauthorized signal and throws on 401', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ status: 401 } as Response)
    vi.stubGlobal('fetch', fetchMock)
    const seen = vi.fn()
    const off = onUnauthorized(seen)

    await expect(apiFetch('/api/v1/health')).rejects.toBeInstanceOf(
      UnauthorizedError,
    )
    expect(seen).toHaveBeenCalledOnce()
    off()
  })
})
