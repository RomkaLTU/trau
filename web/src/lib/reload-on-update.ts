import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'

import { healthQueryOptions, servedBuild } from './health'

export type StaleAction = 'none' | 'reload' | 'notice'

// Which server stamp this window has already spent its one automatic reload on.
// Session-scoped, so the mark dies with the window rather than muting a later
// upgrade.
const RELOADED_KEY = 'trau.web-build.reloaded'

type Store = Pick<Storage, 'getItem' | 'setItem'>

function browserStore(): Store | null {
  try {
    return globalThis.sessionStorage ?? null
  } catch {
    return null
  }
}

export function loadReloadedFor(): string {
  return browserStore()?.getItem(RELOADED_KEY) ?? ''
}

export function recordReloadedFor(build: string): void {
  browserStore()?.setItem(RELOADED_KEY, build)
}

// A focused checkbox carries a value of its own and would otherwise hold the
// page on a dead bundle for as long as it keeps the focus.
const TEXT_INPUT_TYPES = new Set([
  '',
  'text',
  'search',
  'email',
  'url',
  'tel',
  'password',
  'number',
])

// entryInProgress reports whether the user is part-way through an entry. A
// grill reply lives only in the DOM until it is sent, so a reload underneath one
// eats work the hub has never seen.
export function entryInProgress(doc: Document | undefined | null): boolean {
  const el = doc?.activeElement as HTMLElement | null
  if (!el) return false
  const tag = el.tagName
  if (tag === 'TEXTAREA') return hasText((el as HTMLTextAreaElement).value)
  if (tag === 'INPUT') {
    const input = el as HTMLInputElement
    const type = (input.getAttribute('type') ?? '').toLowerCase()
    return TEXT_INPUT_TYPES.has(type) && hasText(input.value)
  }
  if (isEditable(el)) return hasText(el.textContent ?? '')
  return false
}

function hasText(value: string): boolean {
  return value.trim() !== ''
}

function isEditable(el: HTMLElement): boolean {
  if (el.isContentEditable) return true
  const attr = el.getAttribute('contenteditable')
  return attr === '' || attr === 'true'
}

export interface StaleInput {
  pageBuild: string
  serverBuild: string
  reloadedFor: string
  busy: boolean
}

// decideStale settles one successful health answer. A mismatch this window has
// already reloaded for is a reload that did not take, so it earns a notice
// instead of a second attempt.
export function decideStale({
  pageBuild,
  serverBuild,
  reloadedFor,
  busy,
}: StaleInput): StaleAction {
  if (pageBuild === '' || serverBuild === '') return 'none'
  if (pageBuild === serverBuild) return 'none'
  if (reloadedFor === serverBuild) return 'notice'
  if (busy) return 'none'
  return 'reload'
}

// useReloadOnUpdate watches the health poll and returns whether the persistent
// notice is owed. react-query advances dataUpdatedAt on success alone, so a hub
// that is down never runs the effect.
export function useReloadOnUpdate(): boolean {
  const { data, dataUpdatedAt } = useQuery(healthQueryOptions)
  const [notice, setNotice] = useState(false)

  useEffect(() => {
    if (!data) return
    const serverBuild = servedBuild(data)
    const action = decideStale({
      pageBuild: __WEB_BUILD__,
      serverBuild,
      reloadedFor: loadReloadedFor(),
      busy: entryInProgress(globalThis.document),
    })
    if (action === 'notice') {
      setNotice(true)
      return
    }
    if (action !== 'reload') return
    recordReloadedFor(serverBuild)
    globalThis.location.reload()
  }, [data, dataUpdatedAt])

  return notice
}
