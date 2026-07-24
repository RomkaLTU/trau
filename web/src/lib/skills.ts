import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'
import type { FeedEvent } from './events'

export type SkillScope = 'always' | 'auto' | 'manual'

export const SKILL_PHASES = ['build', 'verify', 'repair'] as const
export type SkillPhase = (typeof SKILL_PHASES)[number]

export interface InstalledSkill {
  name: string
  declared_name?: string
  description?: string
  suggested_keywords?: string[]
  invalid?: boolean
  scope: string
  source?: string
  source_type?: string
  skill_path?: string
  pinned: boolean
  loads: number
  last_loaded?: string
}

export interface RecommendedSkill {
  name: string
  package: string
  url: string
}

export interface SkillRule {
  skill: string
  scope: SkillScope
  phases?: string[]
  paths?: string[]
  keywords?: string[]
}

export interface SkillPlan {
  phase: string
  skills: string[]
  source: string
  origins?: Record<string, string>
  fallback?: boolean
}

export interface SkillPhaseCoverage {
  ticket: string
  phase: string
  ts: string
  provider?: string
  planned: string[]
  loaded: string[]
  unknown?: boolean
}

export interface SkillCoverage {
  days: number
  has_data: boolean
  silent_providers?: string[]
  phases: SkillPhaseCoverage[]
}

export interface SkillsResponse {
  repo: string
  project_type: string
  installed: InstalledSkill[]
  recommended: RecommendedSkill[]
  required: string[]
  rules: SkillRule[]
  plan: SkillPlan[]
  coverage: SkillCoverage
  unknown?: string[]
  rules_error?: string
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

export async function saveSkillRules(
  repo: string,
  rules: SkillRule[],
): Promise<SkillsResponse> {
  const res = await apiFetch(`${repoBase(repo)}/rules`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ rules }),
  })
  if (!res.ok) {
    throw new Error(await readError(res, 'saving activation rules failed'))
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

// loadedAgo renders a run-coverage timestamp as a coarse relative age. Token
// calls are stamped zoneless local wall-clock, which Date.parse reads as local.
export function loadedAgo(ts?: string, now: number = Date.now()): string {
  const raw = (ts ?? '').trim()
  if (raw === '') return 'never'
  const ms = Date.parse(raw)
  if (Number.isNaN(ms)) return raw
  const secs = Math.max(0, Math.round((now - ms) / 1000))
  if (secs < 60) return 'just now'
  const mins = Math.floor(secs / 60)
  if (mins < 60) return `${mins}m ago`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours}h ago`
  return `${Math.floor(hours / 24)}d ago`
}

export function toggleRequired(required: string[], name: string): string {
  const clean = required.map((r) => r.trim()).filter((r) => r !== '')
  const next = clean.includes(name)
    ? clean.filter((r) => r !== name)
    : [...clean, name]
  return next.join(',')
}

export function withoutRequired(required: string[], name: string): string {
  return required
    .map((r) => r.trim())
    .filter((r) => r !== '' && r !== name)
    .join(',')
}

export function ruleFor(
  rules: SkillRule[],
  name: string,
): SkillRule | undefined {
  return rules.find((r) => r.skill === name)
}

// scopeOf reads the activation scope a skill behaves under. A REQUIRED_SKILLS
// pin predates the rules file, so a pinned skill with no rule of its own reads
// as Always — the scope its pin already gives it.
export function scopeOf(
  name: string,
  rules: SkillRule[],
  required: string[],
): SkillScope {
  const rule = ruleFor(rules, name)
  if (rule) return rule.scope
  return required.includes(name) ? 'always' : 'auto'
}

export function upsertRule(rules: SkillRule[], rule: SkillRule): SkillRule[] {
  const rest = rules.filter((r) => r.skill !== rule.skill)
  return [...rest, rule].sort((a, b) => a.skill.localeCompare(b.skill))
}

export function parseMatchers(input: string): string[] {
  return input
    .split(/[\s,]+/)
    .map((v) => v.trim())
    .filter((v) => v !== '')
}

// autoNeverMatches flags an auto rule that can never fire: with neither paths
// nor keywords there is nothing for it to match against.
export function autoNeverMatches(rule: SkillRule | undefined): boolean {
  if (!rule || rule.scope !== 'auto') return false
  return (rule.paths?.length ?? 0) === 0 && (rule.keywords?.length ?? 0) === 0
}

export type SkillUsageState = 'loaded' | 'dead' | 'unknown'

// usageState reads a skill's run coverage. Without recoverable evidence — no
// runs, or only providers that never report skill usage — a zero load count
// says nothing, so the skill reads as unknown rather than dead.
export function usageState(
  skill: InstalledSkill,
  coverage: SkillCoverage,
): SkillUsageState {
  if (skill.loads > 0) return 'loaded'
  return coverage.has_data ? 'dead' : 'unknown'
}

// NO_SKILLS_WINDOW_DAYS bounds the no-skills warning to recent runs, so a single
// old warning cannot pin a banner to a repo that has behaved ever since.
export const NO_SKILLS_WINDOW_DAYS = 7

export function latestNoSkillsTicket(
  events: FeedEvent[],
  now: Date = new Date(),
): string | null {
  const cutoff = now.getTime() - NO_SKILLS_WINDOW_DAYS * 86_400_000
  const warnings = events
    .filter((ev) => ev.kind === 'build_no_skills')
    .sort((a, b) => Number(b.id) - Number(a.id))
  for (const ev of warnings) {
    const ticket = ev.fields?.ticket
    const at = Date.parse(ev.ts)
    if (typeof ticket !== 'string' || ticket === '') continue
    if (Number.isNaN(at) || at < cutoff) continue
    return ticket
  }
  return null
}
