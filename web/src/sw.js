const CACHE_NAME = 'trau-__WEB_BUILD__'
const SHELL = __WEB_SHELL__

self.addEventListener('install', (event) => {
  event.waitUntil(caches.open(CACHE_NAME).then((cache) => cache.addAll(SHELL)))
  self.skipWaiting()
})

self.addEventListener('activate', (event) => {
  event.waitUntil(
    (async () => {
      const names = await caches.keys()
      await Promise.all(
        names
          .filter((name) => name !== CACHE_NAME)
          .map((name) => caches.delete(name)),
      )
      await self.clients.claim()
    })(),
  )
})

self.addEventListener('fetch', (event) => {
  const url = new URL(event.request.url)
  const handled =
    event.request.method === 'GET' &&
    url.origin === self.location.origin &&
    !url.pathname.startsWith('/api/')
  if (handled) event.respondWith(networkFirst(event))
})

async function networkFirst(event) {
  const cache = await caches.open(CACHE_NAME)
  try {
    const response = await fetch(event.request)
    if (response.status === 200) {
      event.waitUntil(cache.put(event.request, response.clone()))
    }
    return response
  } catch (offline) {
    const cached = await cache.match(event.request)
    if (cached) return cached
    if (event.request.mode === 'navigate') {
      const shell = await cache.match('/')
      if (shell) return shell
    }
    throw offline
  }
}
