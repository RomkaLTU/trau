import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'
import type { AppURL } from './app-urls'

export interface QAAccount {
  id: number
  label: string
  username: string
  description: string
  source: 'manual' | 'agent'
  secret_set: boolean
  /** The app URL this login opens, or null when it applies to every URL. */
  app_url_id: number | null
}

export interface QAAccountDraft {
  label: string
  username: string
  secret: string
  description: string
  app_url_id: number | null
}

export interface QANotes {
  notes: string
}

async function fetchQAAccounts(repo: string): Promise<QAAccount[]> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/qa/accounts`,
  )
  if (!res.ok) {
    throw new Error(`qa accounts request failed: ${res.status}`)
  }
  return res.json()
}

async function fetchQANotes(repo: string): Promise<QANotes> {
  const res = await apiFetch(`/api/v1/repos/${encodeURIComponent(repo)}/qa/notes`)
  if (!res.ok) {
    throw new Error(`qa notes request failed: ${res.status}`)
  }
  return res.json()
}

async function send(url: string, init: RequestInit, action: string): Promise<void> {
  const res = await apiFetch(url, init)
  if (!res.ok) {
    const detail = (await res.json().catch(() => null)) as {
      error?: string
    } | null
    throw new Error(detail?.error ?? `${action} failed: ${res.status}`)
  }
}

function jsonBody(body: unknown): RequestInit {
  return {
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  }
}

export function createQAAccount(
  repo: string,
  draft: QAAccountDraft,
): Promise<void> {
  return send(
    `/api/v1/repos/${encodeURIComponent(repo)}/qa/accounts`,
    { method: 'POST', ...jsonBody(draft) },
    'qa account create',
  )
}

export function updateQAAccount(
  repo: string,
  id: number,
  draft: QAAccountDraft,
): Promise<void> {
  return send(
    `/api/v1/repos/${encodeURIComponent(repo)}/qa/accounts/${id}`,
    { method: 'PATCH', ...jsonBody(draft) },
    'qa account update',
  )
}

export function deleteQAAccount(repo: string, id: number): Promise<void> {
  return send(
    `/api/v1/repos/${encodeURIComponent(repo)}/qa/accounts/${id}`,
    { method: 'DELETE' },
    'qa account delete',
  )
}

export function writeQANotes(repo: string, notes: string): Promise<void> {
  return send(
    `/api/v1/repos/${encodeURIComponent(repo)}/qa/notes`,
    { method: 'PUT', ...jsonBody({ notes }) },
    'qa notes save',
  )
}

export function draftFor(account: QAAccount | null): QAAccountDraft {
  return {
    label: account?.label ?? '',
    username: account?.username ?? '',
    secret: '',
    description: account?.description ?? '',
    app_url_id: account?.app_url_id ?? null,
  }
}

/** The entry an account is attached to, or null when the repo no longer holds it. */
export function attachedEntry(
  entries: readonly AppURL[],
  id: number | null,
): AppURL | null {
  return entries.find((e) => e.id === id) ?? null
}

export interface QAAccountGroup {
  /** The app URL these logins open, or null for the “any URL” group. */
  entry: AppURL | null
  accounts: QAAccount[]
}

/**
 * Buckets accounts under the app URL they are attached to, in entry order with
 * the unattached ones last. An attachment the repo no longer holds reads as
 * unattached.
 */
export function groupQAAccounts(
  accounts: readonly QAAccount[],
  entries: readonly AppURL[],
): QAAccountGroup[] {
  return [
    ...entries.map((entry) => ({
      entry,
      accounts: accounts.filter((a) => a.app_url_id === entry.id),
    })),
    {
      entry: null,
      accounts: accounts.filter(
        (a) => attachedEntry(entries, a.app_url_id) === null,
      ),
    },
  ]
}

export const qaAccountsQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['qa-accounts', repo],
    queryFn: () => fetchQAAccounts(repo),
    enabled: repo !== '',
  })

export const qaNotesQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['qa-notes', repo],
    queryFn: () => fetchQANotes(repo),
    enabled: repo !== '',
  })
