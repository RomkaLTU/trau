import { apiFetch } from './api'
import type { ConfigWrite } from './config'
import type { RepoView } from './instances'

export type TrackerProvider = 'linear' | 'jira' | 'internal'

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
  if (value === 'linear' || value === 'jira' || value === 'internal') return value
  return null
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
  binding: string
}

// A blank secret or Jira field is omitted, keeping whatever value is already stored.
export function trackerConfigWrites(
  provider: TrackerProvider,
  fields: TrackerFields,
): ConfigWrite[] {
  const writes: ConfigWrite[] = [
    { key: 'TRACKER_PROVIDER', value: provider, layer: 'project' },
  ]
  if (provider === 'internal') return writes
  if (provider === 'linear' && fields.linearKey.trim() !== '') {
    writes.push({ key: 'LINEAR_API_KEY', value: fields.linearKey, layer: 'project' })
  }
  if (provider === 'jira') {
    if (fields.jiraBaseUrl.trim() !== '') {
      writes.push({ key: 'JIRA_BASE_URL', value: fields.jiraBaseUrl.trim(), layer: 'project' })
    }
    if (fields.jiraEmail.trim() !== '') {
      writes.push({ key: 'JIRA_EMAIL', value: fields.jiraEmail.trim(), layer: 'project' })
    }
    if (fields.jiraToken.trim() !== '') {
      writes.push({ key: 'JIRA_API_TOKEN', value: fields.jiraToken, layer: 'project' })
    }
  }
  if (fields.binding.trim() !== '') {
    writes.push({ key: 'LINEAR_TEAM', value: fields.binding.trim(), layer: 'project' })
  }
  return writes
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

export type TestState = 'idle' | 'testing' | 'ok' | 'fail'

export function trackerCanContinue(
  provider: TrackerProvider | null,
  binding: string,
  test: TestState,
): boolean {
  if (provider === 'internal') return true
  if (provider === null) return false
  return binding.trim() !== '' && test === 'ok'
}

const SECRET_MASK = '••••••••••••  (set — enter to replace)'

export function secretPlaceholder(hasExisting: boolean, fallback: string): string {
  return hasExisting ? SECRET_MASK : fallback
}
