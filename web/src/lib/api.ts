import { authHeaders, reportUnauthorized } from './auth'

export class UnauthorizedError extends Error {
  constructor() {
    super('unauthorized')
    this.name = 'UnauthorizedError'
  }
}

export async function apiFetch(
  input: string,
  init: RequestInit = {},
): Promise<Response> {
  const res = await fetch(input, { ...init, headers: authHeaders(init.headers) })
  if (res.status === 401) {
    reportUnauthorized()
    throw new UnauthorizedError()
  }
  return res
}
