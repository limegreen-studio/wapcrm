// Shows web push alerts and opens the right chat when one is tapped.
self.addEventListener('install', () => self.skipWaiting())
self.addEventListener('activate', event => event.waitUntil(self.clients.claim()))

self.addEventListener('push', event => {
  if (!event.data) return
  let data
  try {
    data = event.data.json()
  } catch {
    return
  }
  event.waitUntil(self.registration.showNotification(data.title || 'CRM', {
    body: data.body || '',
    icon: new URL('icons/icon-192.png', self.registration.scope).href,
    tag: data.url || undefined,
    data: { url: data.url || '/' }
  }))
})

self.addEventListener('notificationclick', event => {
  event.notification.close()
  const scope = new URL(self.registration.scope)
  const target = new URL(`${scope.pathname.replace(/\/$/, '')}${event.notification.data?.url || '/'}`, scope.origin).href
  event.waitUntil(self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then(windows => {
    for (const client of windows) {
      if ('focus' in client && 'navigate' in client) {
        return client.navigate(target).then(c => (c || client).focus())
      }
    }
    return self.clients.openWindow(target)
  }))
})
