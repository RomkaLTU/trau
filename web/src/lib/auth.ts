type Store = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

const TOKEN_KEY = 'trau.serve.token'

function browserStore(): Store | null {
  try {
    return globalThis.localStorage ?? null
  } catch {
    return null
  }
}

let token = browserStore()?.getItem(TOKEN_KEY) ?? ''

export function serveToken(): string {
  return token
}

export function setServeToken(next: string): void {
  token = next
  browserStore()?.setItem(TOKEN_KEY, next)
}

export function clearServeToken(): void {
  token = ''
  browserStore()?.removeItem(TOKEN_KEY)
}

export function authHeaders(init?: HeadersInit): Headers {
  const headers = new Headers(init)
  if (token !== '') {
    headers.set('Authorization', `Bearer ${token}`)
  }
  return headers
}

type Listener = () => void

const unauthorizedListeners = new Set<Listener>()

export function onUnauthorized(fn: Listener): () => void {
  unauthorizedListeners.add(fn)
  return () => unauthorizedListeners.delete(fn)
}

export function reportUnauthorized(): void {
  for (const fn of unauthorizedListeners) fn()
}
