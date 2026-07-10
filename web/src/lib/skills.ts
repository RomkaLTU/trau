import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'
import type { FeedEvent } from './events'

export interface InstalledSkill {
  name: string
  source?: string
  source_type?: string
  skill_path?: string
  pinned: boolean
}

export interface RecommendedSkill {
  name: string
  package: string
  url: string
}

export interface SkillsResponse {
  repo: string
  project_type: string
  installed: InstalledSkill[]
  recommended: RecommendedSkill[]
  required: string[]
}

export interface SkillSearchResult {
  id: string
  skill_id: string
  name: string
  installs: number
  source: string
  url: string
}

export interface SkillsSearchResponse {
  query: string
  results: SkillSearchResult[]
  unavailable?: boolean
}

const SKILLS_PAGE_BASE = 'https://skills.sh/'

function repoBase(repo: string): string {
  return `/api/v1/repos/${encodeURIComponent(repo)}/skills`
}

async function fetchSkills(repo: string): Promise<SkillsResponse> {
  const res = await apiFetch(repoBase(repo))
  if (!res.ok) {
    throw new Error(`skills request failed: ${res.status}`)
  }
  return res.json()
}

async function fetchSkillsSearch(
  repo: string,
  query: string,
  owner: string,
): Promise<SkillsSearchResponse> {
  const params = new URLSearchParams({ q: query })
  if (owner) params.set('owner', owner)
  const res = await apiFetch(`${repoBase(repo)}/search?${params.toString()}`)
  if (!res.ok) {
    throw new Error(`skills search failed: ${res.status}`)
  }
  return res.json()
}

async function readError(res: Response, fallback: string): Promise<string> {
  const detail = (await res.json().catch(() => null)) as { error?: string } | null
  return detail?.error ?? `${fallback}: ${res.status}`
}

export async function installSkill(
  repo: string,
  pkg: string,
): Promise<SkillsResponse> {
  const res = await apiFetch(repoBase(repo), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ package: pkg }),
  })
  if (!res.ok) {
    throw new Error(await readError(res, 'install failed'))
  }
  return res.json()
}

export async function removeSkill(
  repo: string,
  name: string,
): Promise<SkillsResponse> {
  const res = await apiFetch(`${repoBase(repo)}/${encodeURIComponent(name)}`, {
    method: 'DELETE',
  })
  if (!res.ok) {
    throw new Error(await readError(res, 'remove failed'))
  }
  return res.json()
}

export const skillsQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['skills', repo],
    queryFn: () => fetchSkills(repo),
    enabled: repo !== '',
  })

export const skillsSearchQueryOptions = (
  repo: string,
  query: string,
  owner = '',
) =>
  queryOptions({
    queryKey: ['skills-search', repo, query, owner],
    queryFn: () => fetchSkillsSearch(repo, query.trim(), owner.trim()),
    enabled: repo !== '' && query.trim() !== '',
    staleTime: 60_000,
  })

export function skillPageUrl(source?: string): string | null {
  const s = (source ?? '').trim()
  return s ? SKILLS_PAGE_BASE + s : null
}

export function toggleRequired(required: string[], name: string): string {
  const clean = required.map((r) => r.trim()).filter((r) => r !== '')
  const next = clean.includes(name)
    ? clean.filter((r) => r !== name)
    : [...clean, name]
  return next.join(',')
}

export function latestNoSkillsTicket(events: FeedEvent[]): string | null {
  const warnings = events
    .filter((ev) => ev.kind === 'build_no_skills')
    .sort((a, b) => Number(b.id) - Number(a.id))
  for (const ev of warnings) {
    const ticket = ev.fields?.ticket
    if (typeof ticket === 'string' && ticket !== '') return ticket
  }
  return null
}
