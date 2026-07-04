import { useCallback, useEffect, useState } from 'react'

import { describeRecapItem, type RecapItem } from '@/lib/recap'

const PREF_KEY = 'trau:notifications'

export type PermissionState = 'unsupported' | 'default' | 'granted' | 'denied'

export function notificationsSupported(): boolean {
  return typeof window !== 'undefined' && 'Notification' in window
}

export function currentPermission(): PermissionState {
  if (!notificationsSupported()) return 'unsupported'
  return Notification.permission as PermissionState
}

function loadPref(): boolean {
  try {
    return localStorage.getItem(PREF_KEY) === '1'
  } catch {
    return false
  }
}

function savePref(on: boolean) {
  try {
    localStorage.setItem(PREF_KEY, on ? '1' : '0')
  } catch {
    // best-effort: a locked-down storage never blocks the UI
  }
}

// fireStateNotification surfaces one state change worth interrupting for. The tag
// is the event key so a reconnect that re-delivers the same event coalesces onto
// the existing notification rather than stacking a duplicate.
export function fireStateNotification(item: RecapItem) {
  if (currentPermission() !== 'granted') return
  try {
    new Notification(item.repo, { body: describeRecapItem(item), tag: item.key })
  } catch {
    // notifications are advisory; a construction failure must not break the feed
  }
}

export interface Notifications {
  supported: boolean
  permission: PermissionState
  enabled: boolean
  enable: () => void
  disable: () => void
}

// useNotifications owns the notification toggle: the persisted opt-in plus the
// live permission grant. Notifications only fire when enabled and granted, so the
// two are surfaced together for the settings UI to reflect.
export function useNotifications(): Notifications {
  const [permission, setPermission] = useState<PermissionState>(currentPermission)
  const [enabled, setEnabled] = useState<boolean>(loadPref)

  useEffect(() => {
    savePref(enabled)
  }, [enabled])

  const enable = useCallback(() => {
    if (!notificationsSupported()) return
    if (Notification.permission === 'granted') {
      setPermission('granted')
      setEnabled(true)
      return
    }
    void Notification.requestPermission().then((res) => {
      setPermission(res as PermissionState)
      setEnabled(res === 'granted')
    })
  }, [])

  const disable = useCallback(() => setEnabled(false), [])

  return {
    supported: notificationsSupported(),
    permission,
    enabled: enabled && permission === 'granted',
    enable,
    disable,
  }
}
