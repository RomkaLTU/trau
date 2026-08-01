import { apiFetch } from './api'
import { createAppURL } from './app-urls'
import type { ConfigWrite } from './config'
import type { RepoView } from './instances'
import { addProjectRepo, createProject, projectForRoot } from './projects'
import { createQAAccount } from './qa'

export type TrackerProvider = 'linear' | 'jira' | 'azure' | 'internal'

export type FindingState = 'ok' | 'warn' | 'missing' | 'info'

export interface InspectCredential {
  provider: string
  layer: string
}

export interface DetectionFinding {
  label: string
  value: string
  state: FindingState
  detail?: string
}

export interface InspectPrefill {
  provider: string
  team: string
  ready_label: string
  epic_flow: boolean
}

export interface RepoInspection {
  path: string
  repo_name: string
  has_trau_ini: boolean
  detected_provider?: string
  credentials: InspectCredential[]
  default_branch: string
  findings: DetectionFinding[]
  prefill?: InspectPrefill
}

export interface Team {
  key: string
  name: string
}

export interface TestConnectionResponse {
  ok: boolean
  issues_visible?: number
  teams?: Team[]
  error?: string
  hint?: string
}

// refused marks a 403 (SERVE_ALLOW_REGISTER) so the path step can render its own callout.
export class InspectError extends Error {
  refused: boolean
  constructor(message: string, refused: boolean) {
    super(message)
    this.name = 'InspectError'
    this.refused = refused
  }
}

async function readError(res: Response, fallback: string): Promise<string> {
  const detail = (await res.json().catch(() => null)) as { error?: string } | null
  return detail?.error ?? `${fallback}: ${res.status}`
}

export async function inspectRepo(path: string): Promise<RepoInspection> {
  const res = await apiFetch('/api/v1/repos/inspect', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path }),
  })
  if (!res.ok) {
    throw new InspectError(await readError(res, 'inspect failed'), res.status === 403)
  }
  return res.json()
}

// Registers with the inline seed sync skipped so the wizard drives its own sync step.
export async function registerForOnboarding(path: string): Promise<RepoView> {
  const res = await apiFetch('/api/v1/repos', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path, sync: false }),
  })
  if (!res.ok) {
    throw new Error(await readError(res, 'register failed'))
  }
  return res.json()
}

export interface MemberRepo {
  repo: string
  root: string
  project: string
  inspection: RepoInspection
}

// Inspects before registering, so a path the hub would refuse never reaches the
// registration store. Registration backs the repo with a project, which is what
// the tracker step configures.
export async function onboardPath(path: string): Promise<MemberRepo> {
  const inspection = await inspectRepo(path)
  const view = await registerForOnboarding(path)
  return {
    repo: view.name,
    root: view.root,
    project: await projectForRoot(view.root),
    inspection,
  }
}

// Creates the project on the first call. Callers only group two or more repos —
// the hub renders a one-member project as a bare repo anyway.
export async function joinProject(
  id: string | null,
  name: string,
  roots: string[],
): Promise<string> {
  const project = id ?? (await createProject(name)).id
  for (const root of roots) {
    await addProjectRepo(project, root)
  }
  return project
}

export interface TestConnectionInput {
  repo?: string
  api_key?: string
  base_url?: string
  email?: string
  api_token?: string
}

export async function testTracker(
  provider: TrackerProvider,
  body: TestConnectionInput,
): Promise<TestConnectionResponse> {
  const res = await apiFetch(
    `/api/v1/trackers/${encodeURIComponent(provider)}/test-connection`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    },
  )
  if (!res.ok) {
    throw new Error(await readError(res, 'connection test failed'))
  }
  return res.json()
}

export async function ensureGitignore(repo: string): Promise<void> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/gitignore`,
    { method: 'POST' },
  )
  if (!res.ok) {
    throw new Error(await readError(res, 'gitignore update failed'))
  }
}

export type WizardStepId =
  | 'path'
  | 'detect'
  | 'tracker'
  | 'essentials'
  | 'appurls'
  | 'sync'
  | 'done'

export interface WizardStep {
  id: WizardStepId
  label: string
}

export const WIZARD_STEPS: WizardStep[] = [
  { id: 'path', label: 'repo path' },
  { id: 'detect', label: 'detection' },
  { id: 'tracker', label: 'tracker' },
  { id: 'essentials', label: 'essentials' },
  { id: 'appurls', label: 'app urls' },
  { id: 'sync', label: 'seed sync' },
  { id: 'done', label: 'ready' },
]

export function stepIndex(id: WizardStepId): number {
  return WIZARD_STEPS.findIndex((s) => s.id === id)
}

export function stepLabel(id: WizardStepId): string {
  return WIZARD_STEPS.find((s) => s.id === id)?.label ?? id
}

export function laterStep(a: WizardStepId, b: WizardStepId): WizardStepId {
  return stepIndex(a) >= stepIndex(b) ? a : b
}

function normalizeProvider(value: string | undefined): TrackerProvider | null {
  switch (value) {
    case 'linear':
    case 'jira':
    case 'azure':
    case 'internal':
      return value
    default:
      return null
  }
}

export function preselectProvider(inspection: RepoInspection): TrackerProvider | null {
  const detected = normalizeProvider(
    inspection.prefill?.provider ?? inspection.detected_provider,
  )
  if (detected) return detected
  const cred = inspection.credentials.find((c) => c.layer !== 'none')
  return normalizeProvider(cred?.provider)
}

export type CredentialLayer = 'project' | 'user'

export function credentialLayer(
  inspection: RepoInspection,
  provider: TrackerProvider,
): CredentialLayer | null {
  const cred = inspection.credentials.find(
    (c) => c.provider === provider && c.layer !== 'none',
  )
  if (cred?.layer === 'project' || cred?.layer === 'user') return cred.layer
  return null
}

export interface TrackerFields {
  linearKey: string
  jiraBaseUrl: string
  jiraEmail: string
  jiraToken: string
  azureOrgUrl: string
  azurePat: string
  binding: string
}

// The keys the project's tracker is configured with. A blank secret or Jira
// field is omitted, keeping whatever value the project already stores.
export function trackerConfigValues(
  provider: TrackerProvider,
  fields: TrackerFields,
): Record<string, string> {
  const keys: Record<string, string> = { TRACKER_PROVIDER: provider }
  if (provider === 'internal') return keys
  if (provider === 'linear' && fields.linearKey.trim() !== '') {
    keys.LINEAR_API_KEY = fields.linearKey
  }
  if (provider === 'jira') {
    if (fields.jiraBaseUrl.trim() !== '') keys.JIRA_BASE_URL = fields.jiraBaseUrl.trim()
    if (fields.jiraEmail.trim() !== '') keys.JIRA_EMAIL = fields.jiraEmail.trim()
    if (fields.jiraToken.trim() !== '') keys.JIRA_API_TOKEN = fields.jiraToken
  }
  if (provider === 'azure') {
    if (fields.azureOrgUrl.trim() !== '') keys.AZURE_ORG_URL = fields.azureOrgUrl.trim()
    if (fields.azurePat.trim() !== '') keys.AZURE_PAT = fields.azurePat
  }
  if (fields.binding.trim() !== '') {
    keys.LINEAR_TEAM = fields.binding.trim()
  }
  return keys
}

export interface EssentialsFields {
  baseBranch: string
  readyLabel: string
  epicFlow: boolean
}

export function essentialsConfigWrites(fields: EssentialsFields): ConfigWrite[] {
  const writes: ConfigWrite[] = []
  if (fields.baseBranch.trim() !== '') {
    writes.push({ key: 'BASE_BRANCH', value: fields.baseBranch.trim(), layer: 'project' })
  }
  if (fields.readyLabel.trim() !== '') {
    writes.push({ key: 'READY_LABEL', value: fields.readyLabel.trim(), layer: 'project' })
  }
  writes.push({ key: 'EPIC_FLOW', value: fields.epicFlow ? '1' : '0', layer: 'project' })
  return writes
}

export interface AppURLAccountFields {
  label: string
  username: string
  secret: string
  description: string
}

export interface AppURLRowFields {
  url: string
  label: string
  workspace: string
  accounts: AppURLAccountFields[]
}

export function blankAppURLAccount(): AppURLAccountFields {
  return { label: '', username: '', secret: '', description: '' }
}

export function blankAppURLRow(): AppURLRowFields {
  return { url: '', label: '', workspace: '', accounts: [] }
}

function accountTouched(account: AppURLAccountFields): boolean {
  const { label, username, secret, description } = account
  return [label, username, secret, description].some((v) => v.trim() !== '')
}

function rowTouched(row: AppURLRowFields): boolean {
  return (
    [row.url, row.label, row.workspace].some((v) => v.trim() !== '') ||
    row.accounts.some(accountTouched)
  )
}

/** Rows carrying a URL. Leaving every row untouched is how the step is skipped. */
export function appURLRowsToWrite(
  rows: readonly AppURLRowFields[],
): AppURLRowFields[] {
  return rows.filter((r) => r.url.trim() !== '')
}

// A row nobody typed into never blocks the step; the clash is reported on the
// later of the two rows so the first one entered stays the valid one.
export function appURLRowIssue(
  rows: readonly AppURLRowFields[],
  index: number,
): string | null {
  const row = rows[index]
  if (!rowTouched(row)) return null
  if (row.url.trim() === '') return 'a URL is required'
  const workspace = row.workspace.trim()
  const clash = rows
    .slice(0, index)
    .some((r) => r.url.trim() !== '' && r.workspace.trim() === workspace)
  if (clash) {
    return workspace === ''
      ? 'a default app URL is already listed above — give this entry a workspace'
      : `workspace “${workspace}” is already listed above`
  }
  if (row.accounts.some((a) => accountTouched(a) && a.label.trim() === '')) {
    return 'every QA account needs a label'
  }
  return null
}

export function appURLsCanContinue(rows: readonly AppURLRowFields[]): boolean {
  return rows.every((_, i) => appURLRowIssue(rows, i) === null)
}

// Accounts attach to the entry they were entered under, so each URL is created
// before its logins.
export async function writeAppURLRows(
  repo: string,
  rows: readonly AppURLRowFields[],
): Promise<void> {
  for (const row of appURLRowsToWrite(rows)) {
    const entry = await createAppURL(repo, {
      label: row.label.trim(),
      url: row.url.trim(),
      workspace: row.workspace.trim(),
    })
    for (const account of row.accounts.filter(accountTouched)) {
      await createQAAccount(repo, {
        label: account.label.trim(),
        username: account.username.trim(),
        secret: account.secret,
        description: account.description.trim(),
        app_url_id: entry.id,
      })
    }
  }
}

export type TestState = 'idle' | 'testing' | 'ok' | 'fail'

export function trackerCanContinue(
  provider: TrackerProvider | null,
  fields: TrackerFields,
  test: TestState,
): boolean {
  if (provider === 'internal') return true
  if (provider === null) return false
  return fields.binding.trim() !== '' && test === 'ok'
}

// Blank fields are allowed when the repo already stores a credential set the hub
// falls back to; otherwise every field the provider needs must be filled in.
export function trackerCanTest(
  provider: TrackerProvider,
  fields: TrackerFields,
  hasExisting: boolean,
): boolean {
  if (provider === 'internal' || hasExisting) return true
  if (provider === 'linear') return fields.linearKey.trim() !== ''
  if (provider === 'azure') {
    return fields.azureOrgUrl.trim() !== '' && fields.azurePat.trim() !== ''
  }
  return (
    fields.jiraBaseUrl.trim() !== '' &&
    fields.jiraEmail.trim() !== '' &&
    fields.jiraToken.trim() !== ''
  )
}

const SECRET_MASK = '••••••••••••  (set — enter to replace)'

export function secretPlaceholder(hasExisting: boolean, fallback: string): string {
  return hasExisting ? SECRET_MASK : fallback
}
