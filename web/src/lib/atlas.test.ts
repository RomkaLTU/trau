import { afterEach, describe, expect, it, vi } from 'vitest'

import { apiFetch } from './api'
import { generateView, generatedAgo, shortSha } from './atlas'

vi.mock('./api', () => ({ apiFetch: vi.fn() }))

const mockFetch = vi.mocked(apiFetch)

function response(status: number, body: unknown) {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  } as unknown as Response
}

describe('shortSha', () => {
  it('trims a commit to its 7-character short form', () => {
    expect(shortSha('abcdef1234567890')).toBe('abcdef1')
  })

  it('leaves a short or empty commit untouched', () => {
    expect(shortSha('abc')).toBe('abc')
    expect(shortSha('')).toBe('')
  })
})

describe('generatedAgo', () => {
  const now = Date.parse('2026-07-16T12:00:00Z')

  it('reads the zoneless store layout as UTC', () => {
    expect(generatedAgo('2026-07-16 09:00:00', now)).toBe('3h ago')
    expect(generatedAgo('2026-07-16 11:30:00', now)).toBe('30m ago')
    expect(generatedAgo('2026-07-14 12:00:00', now)).toBe('2d ago')
  })

  it('reads RFC3339 and collapses the last minute to "just now"', () => {
    expect(generatedAgo('2026-07-16T11:59:30Z', now)).toBe('just now')
  })

  it('falls back to the raw string when it cannot be parsed', () => {
    expect(generatedAgo('not-a-date', now)).toBe('not-a-date')
  })
})

describe('generateView', () => {
  afterEach(() => mockFetch.mockReset())

  it('resolves a 409 (already in flight) to the same in-progress state', async () => {
    mockFetch.mockResolvedValue(response(409, { error: 'already in progress' }))
    await expect(generateView('acme', 'data-model')).resolves.toBeUndefined()
  })

  it('resolves a 202 accepted', async () => {
    mockFetch.mockResolvedValue(response(202, { status: 'generating' }))
    await expect(generateView('acme', 'data-model')).resolves.toBeUndefined()
  })

  it('throws the server error on other failures', async () => {
    mockFetch.mockResolvedValue(response(500, { error: 'boom' }))
    await expect(generateView('acme', 'data-model')).rejects.toThrow('boom')
  })
})
