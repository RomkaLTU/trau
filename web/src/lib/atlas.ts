import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'

export type AtlasFlavor = 'data-model' | 'app-flows'

export interface AtlasCatalogView {
  id: string
  title: string
  flavor: AtlasFlavor
  has_document: boolean
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
  if (!res.ok) {
    const body = (await res.json().catch(() => null)) as {
      error?: string
    } | null
    throw new Error(body?.error ?? `atlas generate failed: ${res.status}`)
  }
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
) =>
  queryOptions({
    queryKey: ['atlas', repo, view],
    queryFn: () => fetchDocument(repo, view),
    enabled: repo !== '' && view !== '' && hasDocument,
  })
