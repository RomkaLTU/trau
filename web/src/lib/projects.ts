import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'
import type { RepoView } from './instances'

// ProjectView is one logical project: a group of registered repos under a display
// name. Members are repo roots, joined against the repos list by the caller.
export interface ProjectView {
  id: string
  name: string
  repos: string[]
}

export interface ProjectsResponse {
  projects: ProjectView[]
}

async function fetchProjects(): Promise<ProjectsResponse> {
  const res = await apiFetch('/api/v1/projects')
  if (!res.ok) {
    throw new Error(`projects request failed: ${res.status}`)
  }
  return res.json()
}

export const projectsQueryOptions = queryOptions({
  queryKey: ['projects'],
  queryFn: fetchProjects,
  refetchInterval: 5000,
})

// RepoRow is one row of the switcher. A project holding two or more listed repos
// renders as a named group; everything else — a single-member project, a repo no
// project holds — renders as a bare repo.
export interface RepoRow {
  project: ProjectView | null
  repos: RepoView[]
}

// groupRepos folds the projects into an already-ordered repo list. A group takes
// the place of its highest-ranked member, so the caller's ordering — live loops
// first, then most recently used — carries over unchanged.
export function groupRepos(
  ordered: readonly RepoView[],
  projects: readonly ProjectView[],
): RepoRow[] {
  const views = new Map(ordered.map((repo) => [repo.root, repo]))
  const owner = new Map<string, ProjectView>()
  for (const project of projects) {
    const listed = project.repos.filter((root) => views.has(root))
    if (listed.length < 2) continue
    for (const root of listed) owner.set(root, project)
  }
  const rows: RepoRow[] = []
  const grouped = new Set<string>()
  for (const repo of ordered) {
    const project = owner.get(repo.root)
    if (!project) {
      rows.push({ project: null, repos: [repo] })
      continue
    }
    if (grouped.has(project.id)) continue
    grouped.add(project.id)
    rows.push({
      project,
      repos: project.repos.flatMap((root) => views.get(root) ?? []),
    })
  }
  return rows
}

async function errorMessage(res: Response, fallback: string): Promise<string> {
  const detail = (await res.json().catch(() => null)) as {
    error?: string
  } | null
  return detail?.error ?? `${fallback}: ${res.status}`
}

export async function createProject(name: string): Promise<ProjectView> {
  const res = await apiFetch('/api/v1/projects', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name }),
  })
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'create project failed'))
  }
  return res.json()
}

// addProjectRepo moves an already-registered repo — named or addressed by root —
// into the project, out of whichever project held it.
export async function addProjectRepo(
  id: string,
  repo: string,
): Promise<ProjectView> {
  const res = await apiFetch(
    `/api/v1/projects/${encodeURIComponent(id)}/repos`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ repo }),
    },
  )
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'add repo failed'))
  }
  return res.json()
}
