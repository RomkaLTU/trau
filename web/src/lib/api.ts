import { authHeaders, reportUnauthorized } from './auth'
import { reportHubFailure, reportHubReachable } from './connectivity'

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
  let res: Response
  try {
    res = await fetch(input, { ...init, headers: authHeaders(init.headers) })
  } catch (err) {
    reportHubFailure()
    throw err
  }
  reportHubReachable()
  if (res.status === 401) {
    reportUnauthorized()
    throw new UnauthorizedError()
  }
  return res
}
