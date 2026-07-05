import { authHeaders, reportUnauthorized } from './auth'

export interface SSEMessage {
  event: string
  data: string
  id?: string
}

export interface SSEHandlers {
  onMessage: (msg: SSEMessage) => void
  onOpen?: () => void
  onError?: (err?: unknown) => void
}

const RETRY_MS = 3000
const BOUNDARY = /\r?\n\r?\n/

export function splitSSE(buffer: string): { blocks: string[]; rest: string } {
  const blocks: string[] = []
  let rest = buffer
  let m: RegExpExecArray | null
  while ((m = BOUNDARY.exec(rest)) !== null) {
    blocks.push(rest.slice(0, m.index))
    rest = rest.slice(m.index + m[0].length)
  }
  return { blocks, rest }
}

// a block with no data and no explicit event type is a comment-only keep-alive
export function parseSSEBlock(block: string): SSEMessage | null {
  let event = 'message'
  let id: string | undefined
  const data: string[] = []
  for (const raw of block.split('\n')) {
    const line = raw.endsWith('\r') ? raw.slice(0, -1) : raw
    if (line === '' || line.startsWith(':')) continue
    const colon = line.indexOf(':')
    const field = colon === -1 ? line : line.slice(0, colon)
    let value = colon === -1 ? '' : line.slice(colon + 1)
    if (value.startsWith(' ')) value = value.slice(1)
    if (field === 'event') event = value
    else if (field === 'data') data.push(value)
    else if (field === 'id') id = value
  }
  if (data.length === 0 && event === 'message') return null
  return { event, data: data.join('\n'), id }
}

// fetch-based SSE client: EventSource can't carry an Authorization header
export function streamSSE(url: string, handlers: SSEHandlers): () => void {
  const controller = new AbortController()
  let closed = false

  const run = async () => {
    while (!closed) {
      try {
        const res = await fetch(url, {
          headers: authHeaders({ Accept: 'text/event-stream' }),
          signal: controller.signal,
        })
        if (res.status === 401) {
          reportUnauthorized()
          handlers.onError?.()
          return
        }
        if (!res.ok || !res.body) {
          handlers.onError?.()
          await delay(RETRY_MS, controller.signal)
          continue
        }
        handlers.onOpen?.()
        await pump(res.body, handlers.onMessage)
      } catch (err) {
        if (closed) return
        handlers.onError?.(err)
      }
      if (closed) return
      await delay(RETRY_MS, controller.signal)
    }
  }
  void run()

  return () => {
    closed = true
    controller.abort()
  }
}

async function pump(
  body: ReadableStream<Uint8Array>,
  onMessage: (msg: SSEMessage) => void,
): Promise<void> {
  const reader = body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  for (;;) {
    const { value, done } = await reader.read()
    if (done) return
    buffer += decoder.decode(value, { stream: true })
    const { blocks, rest } = splitSSE(buffer)
    buffer = rest
    for (const block of blocks) {
      const msg = parseSSEBlock(block)
      if (msg) onMessage(msg)
    }
  }
}

function delay(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (signal.aborted) return resolve()
    const timer = setTimeout(resolve, ms)
    signal.addEventListener(
      'abort',
      () => {
        clearTimeout(timer)
        resolve()
      },
      { once: true },
    )
  })
}
