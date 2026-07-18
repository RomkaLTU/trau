import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'

export interface PromptPlaceholder {
  name: string
  description?: string
  required?: boolean
}

export interface Prompt {
  name: string
  title: string
  description: string
  placeholders: PromptPlaceholder[]
  default: string
  override: string | null
}

export type EffectiveSource = 'default' | 'global' | 'repo'

export interface RepoPrompt extends Prompt {
  repo_override: string | null
  effective: EffectiveSource
  effective_body: string
}

export interface PromptsResponse {
  prompts: Prompt[]
}

export interface RepoPromptsResponse {
  repo: string
  prompts: RepoPrompt[]
}

async function fetchPrompts(): Promise<PromptsResponse> {
  const res = await apiFetch('/api/v1/prompts')
  if (!res.ok) {
    throw new Error(`prompts request failed: ${res.status}`)
  }
  return res.json()
}

async function fetchRepoPrompts(repo: string): Promise<RepoPromptsResponse> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/prompts`,
  )
  if (!res.ok) {
    throw new Error(`prompts request failed: ${res.status}`)
  }
  return res.json()
}

async function putOverride(url: string, body: string): Promise<void> {
  const res = await apiFetch(url, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ body }),
  })
  if (!res.ok) {
    const detail = (await res.json().catch(() => null)) as {
      error?: string
    } | null
    throw new Error(detail?.error ?? `prompt save failed: ${res.status}`)
  }
}

async function deleteOverride(url: string): Promise<void> {
  const res = await apiFetch(url, { method: 'DELETE' })
  if (!res.ok) {
    const detail = (await res.json().catch(() => null)) as {
      error?: string
    } | null
    throw new Error(detail?.error ?? `prompt reset failed: ${res.status}`)
  }
}

export function writePrompt(name: string, body: string): Promise<void> {
  return putOverride(`/api/v1/prompts/${encodeURIComponent(name)}`, body)
}

export function resetPrompt(name: string): Promise<void> {
  return deleteOverride(`/api/v1/prompts/${encodeURIComponent(name)}`)
}

export function writeRepoPrompt(
  repo: string,
  name: string,
  body: string,
): Promise<void> {
  return putOverride(
    `/api/v1/repos/${encodeURIComponent(repo)}/prompts/${encodeURIComponent(name)}`,
    body,
  )
}

export function resetRepoPrompt(repo: string, name: string): Promise<void> {
  return deleteOverride(
    `/api/v1/repos/${encodeURIComponent(repo)}/prompts/${encodeURIComponent(name)}`,
  )
}

export function globalSeed(p: Prompt): string {
  return p.override ?? p.default
}

export function repoSeed(p: RepoPrompt): string {
  return p.repo_override ?? p.override ?? p.default
}

export function repoResetFallback(p: RepoPrompt): 'global' | 'default' {
  return p.override !== null ? 'global' : 'default'
}

export function matchesPrompt(p: Prompt, query: string): boolean {
  if (query === '') return true
  const q = query.toLowerCase()
  return (
    p.name.toLowerCase().includes(q) ||
    p.title.toLowerCase().includes(q) ||
    p.description.toLowerCase().includes(q)
  )
}

export const promptsQueryOptions = queryOptions({
  queryKey: ['prompts'],
  queryFn: fetchPrompts,
})

export const repoPromptsQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['repo-prompts', repo],
    queryFn: () => fetchRepoPrompts(repo),
    enabled: repo !== '',
  })
