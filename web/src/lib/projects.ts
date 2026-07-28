import { queryOptions, useQuery } from '@tanstack/react-query'

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

// projectForRoot names the project holding a repo root, empty when none does.
export async function projectForRoot(root: string): Promise<string> {
  const { projects } = await fetchProjects()
  return projects.find((p) => p.repos.includes(root))?.id ?? ''
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

// projectAnchor names the repo a project's shared tracker surfaces read from:
// its first listed member. Every member of a project talks to the same tracker,
// so anchoring gives the project one inbox instead of one per repo. A repo no
// project holds — and a single-member project — anchors on itself.
export function projectAnchor(
  repo: string,
  repos: readonly RepoView[],
  projects: readonly ProjectView[],
): string {
  const root = repos.find((r) => r.name === repo)?.root
  const project = root ? projects.find((p) => p.repos.includes(root)) : undefined
  if (!project) return repo
  const names = new Map(repos.map((r) => [r.root, r.name]))
  for (const member of project.repos) {
    const name = names.get(member)
    if (name) return name
  }
  return repo
}

// useProjectRepo resolves a scoped repo to its project's anchor, so the tracker
// inbox and its badge stay on one queue however the switcher moves between
// members.
export function useProjectRepo(repo: string, repos: readonly RepoView[]): string {
  const { data } = useQuery(projectsQueryOptions)
  return projectAnchor(repo, repos, data?.projects ?? [])
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

// ProjectTrackerKey is one key of a project's shared tracker. A secret carries
// no value, only whether one is stored.
export interface ProjectTrackerKey {
  key: string
  value?: string
  secret?: boolean
  set?: boolean
}

export interface ProjectTracker {
  project: string
  repos: string[]
  keys: ProjectTrackerKey[]
}

// writeProjectTracker configures the tracker once for the whole project; the hub
// seeds the keys into every member repo's .trau.ini. A key left out — or a blank
// secret — keeps whatever is already stored.
export async function writeProjectTracker(
  id: string,
  keys: Record<string, string>,
): Promise<ProjectTracker> {
  const res = await apiFetch(
    `/api/v1/projects/${encodeURIComponent(id)}/tracker`,
    {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ keys }),
    },
  )
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'save project tracker failed'))
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
