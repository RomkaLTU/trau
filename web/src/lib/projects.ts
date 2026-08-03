import { queryOptions, useQuery } from '@tanstack/react-query'

import { apiFetch } from './api'
import { removeBlocked, type RepoView } from './instances'
import { matchesTerms } from './settings'

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

// projectNameForRoot names the project holding a repo root, empty when none
// does. A single-member project never forms a group, so its name has nowhere to
// surface but the repo's own row.
export function projectNameForRoot(
  root: string,
  projects: readonly ProjectView[],
): string {
  return projects.find((p) => p.repos.includes(root))?.name ?? ''
}

// RepoRow is one row of a repo list — the switcher's, or the instances page's. A
// project holding two or more listed repos renders as a named group; everything
// else — a single-member project, a repo no project holds — renders as a bare repo.
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

// filterRepoRows narrows the switcher to a query, matched case-insensitively
// against a repo's name or its root. A group whose own name matches keeps every
// member: naming the project reads as picking it, not as sifting it. A blank query
// hands the same rows back, so memoized rows stay put.
export function filterRepoRows(rows: RepoRow[], query: string): RepoRow[] {
  const needle = query.trim().toLowerCase()
  if (needle === '') return rows
  const kept: RepoRow[] = []
  for (const row of rows) {
    if (row.project?.name.toLowerCase().includes(needle)) {
      kept.push(row)
      continue
    }
    const repos = row.repos.filter(
      (repo) =>
        repo.name.toLowerCase().includes(needle) ||
        repo.root.toLowerCase().includes(needle),
    )
    if (repos.length > 0) kept.push({ ...row, repos })
  }
  return kept
}

// repoQualifiers tells same-named repos apart on the line that names them: a name
// only one repo carries is left alone, a name two repos share gains the project
// holding it, and a name their projects cannot tell apart falls back to an
// abbreviated root. Keyed by root, which is unique per hub.
export function repoQualifiers(
  repos: readonly RepoView[],
  projects: readonly ProjectView[],
): Map<string, string> {
  const byName = new Map<string, RepoView[]>()
  for (const repo of repos) {
    const shared = byName.get(repo.name) ?? []
    shared.push(repo)
    byName.set(repo.name, shared)
  }
  const qualifiers = new Map<string, string>()
  for (const shared of byName.values()) {
    if (shared.length < 2) continue
    const byProject = new Map<string, RepoView[]>()
    for (const repo of shared) {
      const name = projectNameForRoot(repo.root, projects)
      const holders = byProject.get(name) ?? []
      holders.push(repo)
      byProject.set(name, holders)
    }
    for (const [project, holders] of byProject) {
      if (project !== '' && holders.length === 1) {
        qualifiers.set(holders[0].root, project)
        continue
      }
      for (const [root, tail] of abbreviateRoots(holders.map((r) => r.root))) {
        qualifiers.set(root, tail)
      }
    }
  }
  return qualifiers
}

// abbreviateRoots is the shortest tail of each root no other root in the set
// ends with, so a row spends only as much of its line on a path as it takes to
// be tellable apart.
function abbreviateRoots(roots: readonly string[]): Map<string, string> {
  const parts = roots.map((root) => root.split('/').filter(Boolean))
  const deepest = Math.max(...parts.map((p) => p.length))
  for (let take = 2; take < deepest; take++) {
    const tails = parts.map((p) => pathTail(p, take))
    if (new Set(tails).size === roots.length) {
      return new Map(roots.map((root, i) => [root, tails[i]]))
    }
  }
  return new Map(roots.map((root) => [root, root]))
}

function pathTail(parts: readonly string[], take: number): string {
  if (parts.length <= take) return `/${parts.join('/')}`
  return `…/${parts.slice(-take).join('/')}`
}

// expandedOnOpen is the switcher's opening disclosure: every project row shut
// but the one holding the scoped repo, so the list stays short without hiding
// where the work currently is. Nothing is persisted — closing the dialog drops
// the state and the next open starts here again.
export function expandedOnOpen(
  rows: readonly RepoRow[],
  repo: string,
): Set<string> {
  const holder = rows.find(
    (row) => row.project !== null && row.repos.some((r) => r.name === repo),
  )
  return new Set(holder?.project ? [holder.project.id] : [])
}

export function toggleExpanded(
  expanded: ReadonlySet<string>,
  id: string,
): Set<string> {
  const next = new Set(expanded)
  if (!next.delete(id)) next.add(id)
  return next
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

// ProjectMembers are the repos a project's work can be started in: its holder's
// identifier and every member the repos list knows, in project order. A repo no
// project holds resolves to none.
export interface ProjectMembers {
  project: string
  members: RepoView[]
}

export function projectMembers(
  repo: string,
  repos: readonly RepoView[],
  projects: readonly ProjectView[],
): ProjectMembers {
  const root = repos.find((r) => r.name === repo)?.root
  const project = root ? projects.find((p) => p.repos.includes(root)) : undefined
  if (!project) return { project: '', members: [] }
  const views = new Map(repos.map((r) => [r.root, r]))
  return {
    project: project.id,
    members: project.repos.flatMap((member) => views.get(member) ?? []),
  }
}

// StartRepoHint is the hub's answer to which member repo a ticket should start
// in: suggested names one of the project's members, reason says what earned it.
export interface StartRepoHint {
  project: string
  ticket?: string
  repos: { name: string; root: string }[]
  suggested: string
  reason: 'remembered' | 'named' | 'recent' | 'first'
}

async function fetchStartRepo(
  project: string,
  ticket: string,
): Promise<StartRepoHint> {
  const res = await apiFetch(
    `/api/v1/projects/${encodeURIComponent(project)}/start-repo?id=${encodeURIComponent(ticket)}`,
  )
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'start repo request failed'))
  }
  return res.json()
}

export function startRepoQueryOptions(project: string, ticket: string) {
  return queryOptions({
    queryKey: ['start-repo', project, ticket],
    queryFn: () => fetchStartRepo(project, ticket),
    enabled: project !== '',
    staleTime: 30_000,
  })
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

export async function renameProject(id: string, name: string): Promise<ProjectView> {
  const res = await apiFetch(`/api/v1/projects/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name }),
  })
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'rename project failed'))
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

async function fetchProjectTracker(id: string): Promise<ProjectTracker> {
  const res = await apiFetch(
    `/api/v1/projects/${encodeURIComponent(id)}/tracker`,
  )
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'project tracker request failed'))
  }
  return res.json()
}

export function projectTrackerQueryOptions(id: string) {
  return queryOptions({
    queryKey: ['project-tracker', id],
    queryFn: () => fetchProjectTracker(id),
    enabled: id !== '',
  })
}

export const PROJECT_DEFAULT_KEYS = ['READY_LABEL', 'EPIC_FLOW']

export interface ProjectDefaults {
  readyLabel: string
  epicFlow: boolean
}

// A project holding no EPIC_FLOW reads as on, matching what its members resolve
// today from the shipped default — a fallback of off would show a state no repo
// is in and cost a round trip to actually turn the flow off.
export function projectDefaults(
  tracker: ProjectTracker | undefined,
): ProjectDefaults {
  const value = (key: string) =>
    tracker?.keys.find((k) => k.key === key)?.value ?? ''
  return {
    readyLabel: value('READY_LABEL'),
    epicFlow: value('EPIC_FLOW') !== '0',
  }
}

// projectDefaultsModified is the nav's dot: whether the project answers either
// field itself rather than leaving both to the defaults.
export function projectDefaultsModified(
  tracker: ProjectTracker | undefined,
): boolean {
  const keys = tracker?.keys ?? []
  return keys.some(
    (k) => PROJECT_DEFAULT_KEYS.includes(k.key) && (k.value ?? '') !== '',
  )
}

// Only the fields the user edited are written: a key left out of the request
// keeps whatever the project already stores, so saving one field never restates
// the other. A blank ready label is an edit like any other — it drops the
// project's answer and takes the key back off the members holding it on the
// project's behalf.
export function projectDefaultKeys(
  edited: Partial<ProjectDefaults>,
): Record<string, string> {
  const keys: Record<string, string> = {}
  if (edited.readyLabel !== undefined) {
    keys.READY_LABEL = edited.readyLabel.trim()
  }
  if (edited.epicFlow !== undefined) {
    keys.EPIC_FLOW = edited.epicFlow ? '1' : '0'
  }
  return keys
}

const PROJECT_DEFAULT_TERMS = [
  'project defaults',
  'ready label',
  'ready_label',
  'epic flow',
  'epic_flow',
]

export function matchesProjectDefaults(query: string): boolean {
  return matchesTerms(query, PROJECT_DEFAULT_TERMS)
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

// ProjectRemovalRepo is one member a removal reached; reason is the hub's refusal
// on a member that stayed.
export interface ProjectRemovalRepo {
  name: string
  root: string
  reason?: string
}

// ProjectRemoval is the outcome of removing a project's repos. project is the
// pre-removal snapshot, so the caller can name what it acted on even once the group
// is gone, and blocked carries the members the hub kept: the removal is partial
// rather than all-or-nothing, so a success can still leave members behind.
export interface ProjectRemoval {
  project: ProjectView
  removed: ProjectRemovalRepo[]
  blocked: ProjectRemovalRepo[]
  project_deleted: boolean
}

// removeProjectRepos takes every member repo off the hub in one call, the same
// removal the per-repo Remove button makes: nothing on disk is touched, and running
// trau in a member again brings it back.
export async function removeProjectRepos(id: string): Promise<ProjectRemoval> {
  const res = await apiFetch(
    `/api/v1/projects/${encodeURIComponent(id)}?forget=1`,
    { method: 'DELETE' },
  )
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'remove project failed'))
  }
  return res.json()
}

// BlockedMember is a member the hub would keep, with the reason the row already
// shows for it.
export interface BlockedMember {
  name: string
  reason: string
}

// RemovalPlan is what the confirm dialog names before the click: the members a
// project-wide removal takes, and the ones it would leave with the reason each is
// kept for. It reads the repo list the page already holds, so the hub stays the
// authority — a member removable here can still come back blocked in the outcome.
export interface RemovalPlan {
  removing: string[]
  blocked: BlockedMember[]
}

export function removalPlan(repos: readonly RepoView[]): RemovalPlan {
  const plan: RemovalPlan = { removing: [], blocked: [] }
  for (const repo of repos) {
    const reason = removeBlocked(repo)
    if (reason === null) {
      plan.removing.push(repo.name)
      continue
    }
    plan.blocked.push({ name: repo.name, reason })
  }
  return plan
}

// removalSummary is what a removal's toast says: the count that left when the folder
// cleared, or which members stayed and why when the hub kept some.
export function removalSummary({
  project,
  removed,
  blocked,
}: ProjectRemoval): string {
  if (blocked.length === 0) {
    const repos = removed.length === 1 ? 'repo' : 'repos'
    return `${removed.length} ${repos} removed from ${project.name}`
  }
  const stayed = blocked
    .map((repo) => `${repo.name} stayed (${repo.reason})`)
    .join(', ')
  const total = removed.length + blocked.length
  return `${removed.length} of ${total} removed from ${project.name} — ${stayed}`
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
