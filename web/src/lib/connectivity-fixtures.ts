import { vi } from 'vitest'

import { reportHubFailure } from './connectivity'

export function stubHub(listening: () => boolean) {
  vi.stubGlobal('fetch', async () => {
    if (!listening()) throw new TypeError('failed to fetch')
    return new Response('{}', { status: 200 })
  })
}

export function killHub() {
  reportHubFailure()
  reportHubFailure()
  reportHubFailure()
}
