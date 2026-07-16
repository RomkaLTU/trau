import { keepPreviousData, queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'

export type AtlasFlavor = 'data-model' | 'app-flows'

export interface AtlasCatalogView {
  id: string
  title: string
  flavor: AtlasFlavor
  has_document: boolean
  generating: boolean
  version: number
  commit: string
  generated_at: string
  cost_usd: number | null
  error: string
  stale: number
}

export interface AtlasCatalog {
  views: AtlasCatalogView[]
}

export interface AtlasDocumentResponse {
  view: string
  version: number
  commit: string
  generated_at: string
  cost_usd: number
  document: unknown
}

export type Cardinality = '1:1' | '1:N' | 'N:M'

export interface Field {
  name: string
  type: string
  pk: boolean
}

export interface Entity {
  id: string
  name: string
  domain: string
  fields: Field[]
}

export interface Relationship {
  id: string
  from: string
  to: string
  cardinality: Cardinality
  label: string
}

export interface DataModel {
  entities: Entity[]
  relationships: Relationship[]
}

export type StepKind =
  'ui' | 'http' | 'service' | 'job' | 'queue' | 'db' | 'external' | 'other'

export interface Step {
  id: string
  name: string
  kind: StepKind
}

export interface FlowEdge {
  from: string
  to: string
  label: string
}

export interface Flow {
  id: string
  name: string
  summary: string
  steps: Step[]
  edges: FlowEdge[]
}

export interface AppFlows {
  flows: Flow[]
}

async function fetchCatalog(repo: string): Promise<AtlasCatalog> {
  const res = await apiFetch(`/api/v1/repos/${encodeURIComponent(repo)}/atlas`)
  if (!res.ok) {
    throw new Error(`atlas catalog request failed: ${res.status}`)
  }
  return res.json()
}

async function fetchDocument(
  repo: string,
  view: string,
): Promise<AtlasDocumentResponse> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/atlas/${encodeURIComponent(view)}`,
  )
  if (!res.ok) {
    throw new Error(`atlas document request failed: ${res.status}`)
  }
  return res.json()
}

export async function generateView(repo: string, view: string): Promise<void> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/atlas/${encodeURIComponent(view)}/generate`,
    { method: 'POST' },
  )
  // 409 means a generation is already in flight (e.g. started in another tab);
  // that resolves to the same in-progress state, not an error.
  if (res.ok || res.status === 409) {
    return
  }
  const body = (await res.json().catch(() => null)) as {
    error?: string
  } | null
  throw new Error(body?.error ?? `atlas generate failed: ${res.status}`)
}

export const atlasCatalogQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['atlas', repo],
    queryFn: () => fetchCatalog(repo),
    refetchInterval: 5000,
    enabled: repo !== '',
  })

export const atlasViewQueryOptions = (
  repo: string,
  view: string,
  hasDocument: boolean,
  version: number,
) =>
  queryOptions({
    queryKey: ['atlas', repo, view, version],
    queryFn: () => fetchDocument(repo, view),
    enabled: repo !== '' && view !== '' && hasDocument,
    // Keep the current diagram rendered while a regeneration lands a new
    // version, so the View never blanks and stable ids swap in place.
    placeholderData: keepPreviousData,
  })

export function shortSha(commit: string): string {
  return commit.slice(0, 7)
}

// generatedAt comes from the store as either zoneless UTC or RFC3339; both are
// normalized to UTC before computing the relative age.
export function generatedAgo(generatedAt: string, now: number = Date.now()): string {
  const ms = parseGeneratedAt(generatedAt)
  if (ms === null) {
    return generatedAt
  }
  const secs = Math.max(0, Math.round((now - ms) / 1000))
  if (secs < 60) return 'just now'
  const mins = Math.floor(secs / 60)
  if (mins < 60) return `${mins}m ago`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours}h ago`
  return `${Math.floor(hours / 24)}d ago`
}

function parseGeneratedAt(s: string): number | null {
  if (s === '') {
    return null
  }
  const iso = s.includes('T') ? s : s.replace(' ', 'T')
  const withZone = /[Z+]|[+-]\d\d:\d\d$/.test(iso) ? iso : `${iso}Z`
  const ms = Date.parse(withZone)
  return Number.isNaN(ms) ? null : ms
}
