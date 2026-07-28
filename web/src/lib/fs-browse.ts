import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'

export interface FSEntry {
  name: string
  path: string
  is_repo: boolean
}

export interface BrowseResponse {
  path: string
  parent: string
  separator: string
  is_repo: boolean
  entries: FSEntry[]
}

export class BrowseError extends Error {
  refused: boolean
  constructor(message: string, refused: boolean) {
    super(message)
    this.name = 'BrowseError'
    this.refused = refused
  }
}

export function isRefusal(err: unknown): boolean {
  return err instanceof BrowseError && err.refused
}

async function fetchBrowse(path: string): Promise<BrowseResponse> {
  const query = path === '' ? '' : `?path=${encodeURIComponent(path)}`
  const res = await apiFetch(`/api/v1/fs/browse${query}`)
  if (!res.ok) {
    const detail = (await res.json().catch(() => null)) as { error?: string } | null
    throw new BrowseError(
      detail?.error ?? `browse failed: ${res.status}`,
      res.status === 403,
    )
  }
  return res.json()
}

export const browseQueryOptions = (path: string) =>
  queryOptions({
    queryKey: ['fs-browse', path],
    queryFn: () => fetchBrowse(path),
    staleTime: 15_000,
    retry: false,
  })

export interface Crumb {
  label: string
  path: string
}

// Keeps the volume prefix whole so a Windows path opens at `C:\` rather than a
// bare backslash.
export function breadcrumbs(path: string, separator: string): Crumb[] {
  if (path === '') return []
  const cut = path.indexOf(separator)
  if (cut < 0) return [{ label: path, path }]
  const root = path.slice(0, cut + separator.length)
  const crumbs: Crumb[] = [{ label: root, path: root }]
  let current = root
  for (const segment of path.slice(cut + separator.length).split(separator)) {
    if (segment === '') continue
    current = current.endsWith(separator)
      ? current + segment
      : current + separator + segment
    crumbs.push({ label: segment, path: current })
  }
  return crumbs
}
