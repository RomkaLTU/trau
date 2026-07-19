import { afterEach, describe, expect, it, vi } from 'vitest'

import { uploadAttachment, uploadAttachments } from './attachments'
import { clearServeToken, setServeToken } from './auth'

afterEach(() => {
  clearServeToken()
  vi.unstubAllGlobals()
})

function jsonResponse(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  } as Response
}

function png(name: string): File {
  return new File([new Uint8Array([1, 2, 3])], name, { type: 'image/png' })
}

function uploadedFile(init: RequestInit): File {
  return (init.body as FormData).get('file') as File
}

describe('uploadAttachment', () => {
  it('posts the file as multipart with the bearer credential and no JSON type', async () => {
    setServeToken('sekret')
    const fetchMock = vi
      .fn()
      .mockResolvedValue(
        jsonResponse(201, {
          id: 7,
          url: '/api/v1/repos/acme/attachments/7',
          filename: 'shot.png',
          mime_type: 'image/png',
        }),
      )
    vi.stubGlobal('fetch', fetchMock)

    const file = new File([new Uint8Array([1, 2, 3])], 'shot.png', {
      type: 'image/png',
    })
    const uploaded = await uploadAttachment('acme', file)

    expect(uploaded.url).toBe('/api/v1/repos/acme/attachments/7')
    const [input, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(input).toBe('/api/v1/repos/acme/attachments')
    expect(init.method).toBe('POST')
    expect(init.body).toBeInstanceOf(FormData)
    expect((init.body as FormData).get('file')).toBeInstanceOf(File)
    const headers = new Headers(init.headers)
    expect(headers.get('Authorization')).toBe('Bearer sekret')
    expect(headers.has('Content-Type')).toBe(false)
  })

  it('encodes the repo name into the path', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse(201, { id: 1, url: 'u', filename: '', mime_type: '' }))
    vi.stubGlobal('fetch', fetchMock)

    await uploadAttachment('acme/api', new File([], 'x.png', { type: 'image/png' }))

    expect((fetchMock.mock.calls[0] as [string])[0]).toBe(
      '/api/v1/repos/acme%2Fapi/attachments',
    )
  })

  it('surfaces the hub error message when the upload is rejected', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(jsonResponse(400, { error: 'only image uploads are supported' })),
    )

    await expect(
      uploadAttachment('acme', new File([], 'notes.txt', { type: 'text/plain' })),
    ).rejects.toThrow('only image uploads are supported')
  })
})

describe('uploadAttachments', () => {
  it('reports a batch in file order even when a later file resolves first', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_input: string, init: RequestInit) => {
        const file = uploadedFile(init)
        await new Promise((resolve) =>
          setTimeout(resolve, file.name === 'first.png' ? 20 : 0),
        )
        return jsonResponse(201, {
          id: 1,
          url: `/api/v1/repos/acme/attachments/${file.name}`,
          filename: file.name,
          mime_type: 'image/png',
        })
      }),
    )

    const { uploaded, errors } = await uploadAttachments('acme', [
      png('first.png'),
      png('second.png'),
      png('third.png'),
    ])

    expect(uploaded.map((att) => att.filename)).toEqual([
      'first.png',
      'second.png',
      'third.png',
    ])
    expect(errors).toEqual([])
  })

  it('keeps the accepted images and surfaces the rejected file separately', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_input: string, init: RequestInit) => {
        const file = uploadedFile(init)
        if (file.type !== 'image/png') {
          return jsonResponse(400, {
            error: 'only image uploads are supported',
          })
        }
        return jsonResponse(201, {
          id: 1,
          url: `/api/v1/repos/acme/attachments/${file.name}`,
          filename: file.name,
          mime_type: 'image/png',
        })
      }),
    )

    const { uploaded, errors } = await uploadAttachments('acme', [
      png('shot.png'),
      new File([], 'notes.txt', { type: 'text/plain' }),
      png('other.png'),
    ])

    expect(uploaded.map((att) => att.filename)).toEqual([
      'shot.png',
      'other.png',
    ])
    expect(errors).toEqual(['only image uploads are supported'])
  })
})
