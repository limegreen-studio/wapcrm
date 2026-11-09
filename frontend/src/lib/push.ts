import { pushService } from '@/services/api'
import { unwrapResponse } from '@/lib/api-utils'

const basePath = ((window as any).__BASE_PATH__ ?? '').replace(/\/$/, '')

export function pushSupported(): boolean {
  return 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window
}

// Registered on load so the app can be installed to a phone's home screen.
export function registerServiceWorker(): void {
  if (!('serviceWorker' in navigator)) return
  navigator.serviceWorker.register(`${basePath}/sw.js`, { scope: `${basePath}/` }).catch(() => {
    // Alerts stay in-app without it
  })
}

function keyToBytes(base64: string): ArrayBuffer {
  const padded = base64.replace(/-/g, '+').replace(/_/g, '/').padEnd(Math.ceil(base64.length / 4) * 4, '=')
  const raw = atob(padded)
  const buffer = new ArrayBuffer(raw.length)
  const bytes = new Uint8Array(buffer)
  for (let i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i)
  return buffer
}

// Asks permission and subscribes this device. Returns false if refused or
// when the server has no push keys configured.
export async function enablePush(): Promise<boolean> {
  if (!pushSupported()) return false
  if ((await Notification.requestPermission()) !== 'granted') return false
  const { public_key: key } = unwrapResponse<{ public_key: string }>(await pushService.key())
  if (!key) return true // desktop notifications still work while a tab is open
  const registration = await navigator.serviceWorker.ready
  const subscription = await registration.pushManager.getSubscription()
    ?? await registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: keyToBytes(key) })
  await pushService.subscribe(subscription.toJSON())
  return true
}
