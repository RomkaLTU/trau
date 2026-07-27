// The worker is only emitted by `vite build`, so registering it under `vite dev`
// would just fetch the SPA shell and fail on its MIME type.
export function registerServiceWorker(): void {
  if (!import.meta.env.PROD || !('serviceWorker' in navigator)) return
  window.addEventListener('load', () => {
    void navigator.serviceWorker.register('/sw.js').catch(() => {})
  })
}
