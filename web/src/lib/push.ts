import { useCallback, useEffect, useState } from 'react'

import { apiFetch } from '@/lib/api'
import { currentPermission, type PermissionState } from '@/lib/notifications'

export interface PushKeys {
  p256dh: string
  auth: string
}

export interface PushSubscriptionPayload {
  endpoint: string
  keys: PushKeys
}

export interface PushSendResult {
  sent: number
  pruned: number
}

export function pushSupported(): boolean {
  return (
    typeof window !== 'undefined' &&
    'serviceWorker' in navigator &&
    'PushManager' in window &&
    'Notification' in window
  )
}

// applicationServerKey decodes the hub's VAPID public key — unpadded base64url —
// into the raw bytes PushManager.subscribe insists on.
export function applicationServerKey(key: string): Uint8Array<ArrayBuffer> {
  const base64 = key.replace(/-/g, '+').replace(/_/g, '/')
  const raw = atob(base64.padEnd(Math.ceil(base64.length / 4) * 4, '='))
  const bytes = new Uint8Array(raw.length)
  for (let i = 0; i < raw.length; i += 1) bytes[i] = raw.charCodeAt(i)
  return bytes
}

// subscriptionPayload reshapes a browser subscription into the hub's body. Only
// toJSON() exposes the encryption keys in a serialisable form.
export function subscriptionPayload(
  sub: Pick<PushSubscription, 'endpoint' | 'toJSON'>,
): PushSubscriptionPayload {
  const keys = sub.toJSON().keys ?? {}
  return {
    endpoint: sub.endpoint,
    keys: { p256dh: keys.p256dh ?? '', auth: keys.auth ?? '' },
  }
}

async function pushError(res: Response, fallback: string): Promise<string> {
  const detail = (await res.json().catch(() => null)) as { error?: string } | null
  return detail?.error ?? `${fallback}: ${res.status}`
}

export async function fetchPushKey(): Promise<string> {
  const res = await apiFetch('/api/v1/push/key')
  if (!res.ok) {
    throw new Error(await pushError(res, 'push key request failed'))
  }
  const body = (await res.json()) as { public_key: string }
  return body.public_key
}

export async function savePushSubscription(
  payload: PushSubscriptionPayload,
): Promise<void> {
  const res = await apiFetch('/api/v1/push/subscriptions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  })
  if (!res.ok) {
    throw new Error(await pushError(res, 'push subscribe failed'))
  }
}

export async function dropPushSubscription(endpoint: string): Promise<void> {
  const res = await apiFetch('/api/v1/push/subscriptions', {
    method: 'DELETE',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ endpoint }),
  })
  if (!res.ok) {
    throw new Error(await pushError(res, 'push unsubscribe failed'))
  }
}

export async function sendTestPush(): Promise<PushSendResult> {
  const res = await apiFetch('/api/v1/push/test', { method: 'POST' })
  if (!res.ok) {
    throw new Error(await pushError(res, 'test notification failed'))
  }
  return res.json()
}

// pushRegistration resolves the active worker, or undefined when none is
// registered. navigator.serviceWorker.ready never settles without a
// registration, so the existence check has to come first — under `vite dev` no
// worker is emitted at all.
async function pushRegistration(): Promise<ServiceWorkerRegistration | undefined> {
  if (!pushSupported()) return undefined
  const existing = await navigator.serviceWorker.getRegistration()
  if (!existing) return undefined
  return navigator.serviceWorker.ready
}

async function currentSubscription(): Promise<PushSubscription | null> {
  const reg = await pushRegistration()
  if (!reg) return null
  return reg.pushManager.getSubscription()
}

// pushSubscribed reports whether this browser currently holds a push
// subscription. The in-tab path asks live rather than trusting a mount-time
// snapshot, so enabling push takes effect without a reload.
export async function pushSubscribed(): Promise<boolean> {
  return (await currentSubscription()) !== null
}

export interface PushNotifications {
  supported: boolean
  permission: PermissionState
  subscribed: boolean
  ready: boolean
  enable: () => Promise<void>
  disable: () => Promise<void>
}

// usePushNotifications owns the browser side of Web Push: the live permission
// grant and whether this browser holds a subscription the hub knows about.
// Unlike the in-page notification opt-in there is no stored preference — the
// subscription itself is the state, and it outlives the tab.
export function usePushNotifications(): PushNotifications {
  const [permission, setPermission] = useState<PermissionState>(currentPermission)
  const [subscribed, setSubscribed] = useState(false)
  const [ready, setReady] = useState(false)

  useEffect(() => {
    let live = true
    void currentSubscription().then((sub) => {
      if (!live) return
      setSubscribed(sub !== null)
      setReady(true)
    })
    return () => {
      live = false
    }
  }, [])

  const enable = useCallback(async () => {
    const granted =
      Notification.permission === 'granted'
        ? 'granted'
        : ((await Notification.requestPermission()) as PermissionState)
    setPermission(granted)
    if (granted !== 'granted') {
      throw new Error('notification permission was not granted')
    }
    const reg = await pushRegistration()
    if (!reg) {
      throw new Error('the service worker is not registered yet — reload and try again')
    }
    const key = await fetchPushKey()
    const sub =
      (await reg.pushManager.getSubscription()) ??
      (await reg.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: applicationServerKey(key),
      }))
    await savePushSubscription(subscriptionPayload(sub))
    setSubscribed(true)
  }, [])

  const disable = useCallback(async () => {
    const sub = await currentSubscription()
    setSubscribed(false)
    if (!sub) return
    await sub.unsubscribe()
    await dropPushSubscription(sub.endpoint)
  }, [])

  return {
    supported: pushSupported(),
    permission,
    subscribed,
    ready,
    enable,
    disable,
  }
}
