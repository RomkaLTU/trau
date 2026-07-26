import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'

export type RunDiffStatus = 'added' | 'modified' | 'deleted' | 'renamed'

export interface RunDiffFile {
  path: string
  old_path?: string
  status: RunDiffStatus
  additions: number
  deletions: number
  binary: boolean
  patch: string
}

export interface RunDiff {
  source: 'live' | 'committed'
  base: string
  branch: string
  base_sha: string
  head_sha: string
  truncated: boolean
  files: RunDiffFile[]
}

// firstChangedLine is where an editor opening the file should land: the new-file
// start of the patch's first hunk.
export function firstChangedLine(patch: string): number | undefined {
  const hunk = /^@@ -\d+(?:,\d+)? \+(\d+)/m.exec(patch)
  return hunk ? Number(hunk[1]) : undefined
}

export class RunDiffError extends Error {
  status: number

  constructor(message: string, status: number) {
    super(message)
    this.name = 'RunDiffError'
    this.status = status
  }
}

async function fetchRunDiff(repo: string, ticket: string): Promise<RunDiff> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/runs/${encodeURIComponent(ticket)}/diff`,
  )
  if (!res.ok) {
    throw new RunDiffError(`run diff request failed: ${res.status}`, res.status)
  }
  return res.json()
}

// REFRESH_MS keeps the pane close to the working tree without hammering it: every
// poll shells out to a handful of git processes in the target repo.
const REFRESH_MS = 4000

export const runDiffQueryOptions = (repo: string, ticket: string) =>
  queryOptions({
    queryKey: ['run-diff', repo, ticket],
    queryFn: () => fetchRunDiff(repo, ticket),
    refetchInterval: REFRESH_MS,
    enabled: repo !== '' && ticket !== '',
    retry: false,
  })

type Store = Pick<Storage, 'getItem' | 'setItem'>

const MODE_KEY = 'trau.rundiff.mode'
const TREE_KEY = 'trau.rundiff.tree'

// The file tree only earns a column at lg; below it it stacks on top of the cards,
// so narrow viewports start with it closed.
const WIDE_QUERY = '(min-width: 64rem)'

export type DiffLayout = 'split' | 'inline'

function browserStore(): Store | null {
  try {
    return globalThis.localStorage ?? null
  } catch {
    return null
  }
}

function wideViewport(): boolean {
  return globalThis.matchMedia?.(WIDE_QUERY).matches ?? true
}

export function loadDiffLayout(): DiffLayout {
  return browserStore()?.getItem(MODE_KEY) === 'inline' ? 'inline' : 'split'
}

export function storeDiffLayout(layout: DiffLayout): void {
  browserStore()?.setItem(MODE_KEY, layout)
}

export function loadDiffTreeOpen(): boolean {
  const raw = browserStore()?.getItem(TREE_KEY) ?? null
  return raw === null ? wideViewport() : raw === '1'
}

export function storeDiffTreeOpen(open: boolean): void {
  browserStore()?.setItem(TREE_KEY, open ? '1' : '0')
}
