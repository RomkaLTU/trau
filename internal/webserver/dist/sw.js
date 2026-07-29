const CACHE_NAME = 'trau-gc3f1ee3'
const SHELL = ["/","/assets/index.js","/assets/index.css"]

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

self.addEventListener('push', (event) => {
  const payload = pushPayload(event)
  event.waitUntil(
    self.registration.showNotification(payload.title, {
      body: payload.body,
      tag: payload.tag,
      icon: '/icon-192.png',
      badge: '/icon-192.png',
      data: { url: payload.url },
    }),
  )
})

self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const target = new URL(event.notification.data?.url || '/', self.location.origin)
  event.waitUntil(focusOrOpen(target))
})

function pushPayload(event) {
  const fallback = { title: 'trau', body: '', tag: 'trau', url: '/' }
  if (!event.data) return fallback
  try {
    return { ...fallback, ...event.data.json() }
  } catch (notJSON) {
    return { ...fallback, body: event.data.text() }
  }
}

async function focusOrOpen(target) {
  const windows = await self.clients.matchAll({
    type: 'window',
    includeUncontrolled: true,
  })
  const open = windows.find((client) => new URL(client.url).origin === target.origin)
  if (!open) {
    await self.clients.openWindow(target.href)
    return
  }
  await open.focus()
  if (open.url !== target.href && 'navigate' in open) {
    await open.navigate(target.href)
  }
}

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
