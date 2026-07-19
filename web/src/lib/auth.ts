type Store = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

const TOKEN_KEY = 'trau.serve.token'
const COOKIE_KEY = 'trau_serve_token'

function browserStore(): Store | null {
  try {
    return globalThis.localStorage ?? null
  } catch {
    return null
  }
}

let token = browserStore()?.getItem(TOKEN_KEY) ?? ''
syncCookie()

export function serveToken(): string {
  return token
}

export function setServeToken(next: string): void {
  token = next
  browserStore()?.setItem(TOKEN_KEY, next)
  syncCookie()
}

export function clearServeToken(): void {
  token = ''
  browserStore()?.removeItem(TOKEN_KEY)
  syncCookie()
}

// syncCookie mirrors the bearer token into a same-origin cookie so the browser
// authenticates requests it makes without a header — an <img src> pointing at an
// attachment's bytes — on an exposed bind. It carries the same token the SPA
// already holds, so it grants nothing the Authorization header would not.
function syncCookie(): void {
  if (typeof document === 'undefined') return
  const attrs = 'path=/; SameSite=Strict'
  document.cookie =
    token === ''
      ? `${COOKIE_KEY}=; Max-Age=0; ${attrs}`
      : `${COOKIE_KEY}=${token}; ${attrs}`
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
