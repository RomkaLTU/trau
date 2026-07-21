import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'

export interface QAAccount {
  id: number
  label: string
  username: string
  description: string
  secret_set: boolean
}

export interface QAAccountDraft {
  label: string
  username: string
  secret: string
  description: string
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
  }
}

export function matchesQAAccount(a: QAAccount, query: string): boolean {
  if (query === '') return true
  const q = query.toLowerCase()
  return (
    a.label.toLowerCase().includes(q) ||
    a.username.toLowerCase().includes(q) ||
    a.description.toLowerCase().includes(q)
  )
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
