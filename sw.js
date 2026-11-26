// Service worker: keeps the app itself (page, scripts, the WebAssembly
// engine) available offline and makes later starts instant. Requests to
// the timetable and tariff services are never touched.
const cacheName = 'wasdarfmeinticket-v1';

self.addEventListener('install', () => self.skipWaiting());

self.addEventListener('activate', (event) => {
  event.waitUntil(
    (async () => {
      for (const name of await caches.keys()) {
        if (name !== cacheName) {
          await caches.delete(name);
        }
      }
      await self.clients.claim();
    })(),
  );
});

self.addEventListener('fetch', (event) => {
  const request = event.request;
  if (request.method !== 'GET' || new URL(request.url).origin !== self.location.origin) {
    return;
  }
  // Every page view is the one page, whatever a shared link added to its
  // address.
  const target = request.mode === 'navigate' ? self.registration.scope : request;
  event.respondWith(
    (async () => {
      const cache = await caches.open(cacheName);
      const cached = await cache.match(target);
      // Answer from the cache and refresh it in the background, so an
      // update arrives with the next start.
      const refreshed = fetch(target).then((response) => {
        if (response.ok) {
          cache.put(target, response.clone());
        }
        return response;
      });
      if (cached) {
        event.waitUntil(refreshed.catch(() => {}));
        return cached;
      }
      return refreshed;
    })(),
  );
});
