import { useMemo, useState, type ReactNode } from 'react'
import { useMutation } from '@tanstack/react-query'
import { ArrowRight, Plug } from 'lucide-react'

import { TRACKER_PROVIDERS, trackerProviderMeta } from '@/components/trau/tracker-providers'
import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  bindingUnreachable,
  bindingVisible,
  credentialLayer,
  forgeLabel,
  matchingTeam,
  preselectProvider,
  testTracker,
  trackerCanContinue,
  trackerCanTest,
  trackerConfigValues,
  type RepoInspection,
  type Team,
  type TestState,
  type TrackerFields,
  type TrackerProvider,
} from '@/lib/onboarding'
import { writeProjectTracker } from '@/lib/projects'
import { mappingSpec, type StatusOptionsProbe } from '@/lib/statusmap'
import { TrackerMappingSection } from './step-tracker-mapping'
import { Callout, FieldLabel, Hint, SecretInput, TextInput } from './ui'

const BINDING_LABEL: Partial<Record<TrackerProvider, string>> = {
  linear: 'linear team',
  jira: 'jira project',
  azure: 'azure devops project',
}

const TEST_HINT: Partial<Record<TrackerProvider, string>> = {
  linear: 'Enter the API key to test.',
  jira: 'Enter the site URL, email, and API token to test.',
  azure: 'Enter the organization URL and personal access token to test.',
}

// Mirrors azurePATScopes in internal/tui/onboarding_form.go so the CLI and the
// web wizard ask for the same two scopes.
const AZURE_PAT_SCOPES = 'Work Items (read & write) and Project and Team (read)'
const AZURE_PAT_SETTINGS_URL = 'https://dev.azure.com/_usersSettings/tokens'

// codeHome names the forge the inspected repo is actually on, so this step cannot
// read as if trau pulls code from whichever tracker is picked below. A folder's
// children each answer for themselves, which the detection step already listed.
function codeHome(inspection: RepoInspection): string {
  if (inspection.kind === 'folder') return "each child repo's own remote"
  if (inspection.forge === '') return 'this machine, with no remote configured'
  return forgeLabel(inspection.forge)
}

// Linear grants team access on the API key itself; Jira and Azure DevOps grant it
// through the account and the token's scope, so the remedy differs per provider.
const MISSING_BINDING: Partial<
  Record<TrackerProvider, { title: string; body: (stored: ReactNode) => ReactNode }>
> = {
  linear: {
    title: "The saved team isn't visible to this API key",
    body: (stored) => (
      <>
        The team {stored} is stored in this config, but this API key cannot see it. The key may
        be restricted to specific teams — check its team access under Linear → Settings →
        Security &amp; access → Personal API keys, or pick one of the teams below.
      </>
    ),
  },
  jira: {
    title: "The saved project isn't visible to these credentials",
    body: (stored) => (
      <>
        The project {stored} is stored in this config, but the account these credentials belong
        to cannot see it. Check the project's permissions in Jira, or pick one of the projects
        below.
      </>
    ),
  },
  azure: {
    title: "The saved project isn't visible to this token",
    body: (stored) => (
      <>
        The project {stored} is stored in this config, but this personal access token cannot
        see it. The token may be scoped to a different organization or set of projects — check
        it under User settings → Personal access tokens, or pick one of the projects below.
      </>
    ),
  },
}

function MissingBinding({
  provider,
  binding,
}: {
  provider: TrackerProvider
  binding: string
}) {
  const copy = MISSING_BINDING[provider]
  if (!copy) return null
  return (
    <Callout tone="warn" title={copy.title}>
      {copy.body(<span className="font-mono text-foreground">{binding}</span>)}
    </Callout>
  )
}

// statusOptionsProbe is the payload the mapping section reads its choices with:
// the credential fields the connection test posts, plus the binding they are read
// under. A blank secret still falls back to whatever the named repo stores.
function statusOptionsProbe(
  provider: TrackerProvider,
  fields: TrackerFields,
  repo: string,
): StatusOptionsProbe {
  const azure = provider === 'azure'
  return {
    repo,
    api_key: fields.linearKey.trim() || undefined,
    base_url: (azure ? fields.azureOrgUrl : fields.jiraBaseUrl).trim() || undefined,
    email: fields.jiraEmail.trim() || undefined,
    api_token: (azure ? fields.azurePat : fields.jiraToken).trim() || undefined,
    binding: fields.binding.trim(),
  }
}

function CredsFound() {
  return (
    <span className="rounded-full border border-teal/50 bg-teal/10 px-1.5 py-0.5 font-mono text-[0.6rem] text-teal">
      creds found
    </span>
  )
}

export function StepTracker({
  inspection,
  repo,
  project,
  onBack,
  onContinue,
}: {
  inspection: RepoInspection
  repo: string
  project: string
  onBack: () => void
  onContinue: (provider: TrackerProvider, fields: TrackerFields) => void
}) {
  const [provider, setProvider] = useState<TrackerProvider | null>(() =>
    preselectProvider(inspection),
  )
  const [linearKey, setLinearKey] = useState('')
  const [jiraSite, setJiraSite] = useState('')
  const [jiraEmail, setJiraEmail] = useState('')
  const [jiraToken, setJiraToken] = useState('')
  const [azureOrgUrl, setAzureOrgUrl] = useState('')
  const [azurePat, setAzurePat] = useState('')
  const [binding, setBinding] = useState(inspection.prefill?.team ?? '')
  const [unreachableBinding, setUnreachableBinding] = useState('')
  // Anything that invalidates the fetch behind the mapping — the provider, a
  // credential, the binding — clears it too, so a mapping read under one board
  // never rides a write to another.
  const [mapping, setMapping] = useState<Record<string, string>>({})

  const fields: TrackerFields = {
    linearKey,
    jiraBaseUrl: jiraSite,
    jiraEmail,
    jiraToken,
    azureOrgUrl,
    azurePat,
    binding,
    mapping,
  }

  const test = useMutation({
    mutationFn: (p: TrackerProvider) =>
      testTracker(p, {
        repo,
        api_key: linearKey.trim() || undefined,
        base_url: (p === 'azure' ? azureOrgUrl : jiraSite).trim() || undefined,
        email: jiraEmail.trim() || undefined,
        api_token: (p === 'azure' ? azurePat : jiraToken).trim() || undefined,
      }),
    // A prefilled binding is never auto-replaced: when the credentials cannot reach
    // it the selection is cleared and named, so the re-pick is the user's own.
    onSuccess: (res) => {
      const teams = res.ok ? (res.teams ?? []) : []
      setMapping({})
      if (binding === '') {
        const first = teams[0]
        if (first) setBinding(first.key)
        setUnreachableBinding('')
        return
      }
      const match = matchingTeam(teams, binding)
      if (teams.length > 0 && !match) {
        setUnreachableBinding(binding)
        setBinding('')
        return
      }
      // Adopting the provider's spelling keeps the select showing the team rather
      // than blank when the stored key differs only in case.
      if (match && match.key !== binding) setBinding(match.key)
      setUnreachableBinding('')
    },
  })

  const testState: TestState = test.isPending
    ? 'testing'
    : test.data
      ? test.data.ok
        ? 'ok'
        : 'fail'
      : 'idle'

  const fetchedTeams = useMemo<Team[]>(
    () => (test.data?.ok ? (test.data.teams ?? []) : []),
    [test.data],
  )

  const bindingOptions = useMemo<Team[]>(() => {
    if (fetchedTeams.length > 0) return fetchedTeams
    return binding !== '' ? [{ key: binding, name: binding }] : []
  }, [fetchedTeams, binding])

  const commit = useMutation({
    mutationFn: async () => {
      if (!provider) return
      await writeProjectTracker(project, trackerConfigValues(provider, fields))
    },
    onSuccess: () => provider && onContinue(provider, fields),
  })

  const selected = trackerProviderMeta(provider)
  const spec = provider !== null ? mappingSpec(provider) : null
  const needsBinding = provider !== null && provider !== 'internal'
  const existingLayer = provider !== null ? credentialLayer(inspection, provider) : null
  const hasExisting = existingLayer !== null
  const canTest = provider !== null && trackerCanTest(provider, fields, hasExisting)
  const canContinue = trackerCanContinue(provider, fields, testState, fetchedTeams)
  const bindingReachable = bindingVisible(fetchedTeams, binding)
  const showUnreachable =
    testState === 'ok' && bindingUnreachable(fetchedTeams, binding, unreachableBinding)

  function choose(next: TrackerProvider) {
    if (next === provider) return
    setProvider(next)
    setBinding(next === inspection.prefill?.provider ? (inspection.prefill?.team ?? '') : '')
    setUnreachableBinding('')
    setMapping({})
    test.reset()
  }

  function chooseBinding(next: string) {
    if (next === binding) return
    setBinding(next)
    setMapping({})
  }

  function editCredential(value: string, setter: (v: string) => void) {
    setter(value)
    setMapping({})
    if (testState !== 'idle') {
      setUnreachableBinding('')
      test.reset()
    }
  }

  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-col gap-1.5">
        <h2 className="font-mono text-base text-foreground">Where do the tickets come from?</h2>
        <Hint>
          trau writes <span className="font-mono">TRACKER_PROVIDER</span> explicitly and tests the
          connection before it will sync. One tracker per project — every repo in it inherits
          these keys.
        </Hint>
        <Hint>
          Tickets only. The code stays where its git remote says it does — {codeHome(inspection)}{' '}
          — and nothing on this step changes that.
        </Hint>
      </div>

      <div className="flex flex-col gap-2">
        <FieldLabel htmlFor="tracker-provider">tracker provider</FieldLabel>
        <Select
          value={provider ?? undefined}
          onValueChange={(v) => choose(v as TrackerProvider)}
        >
          <SelectTrigger id="tracker-provider" className="w-full">
            <SelectValue placeholder="Choose a tracker">
              {selected && (
                <>
                  <selected.Icon />
                  <span className="font-mono text-sm text-foreground">{selected.name}</span>
                </>
              )}
            </SelectValue>
          </SelectTrigger>
          <SelectContent>
            {TRACKER_PROVIDERS.map((p) => (
              <SelectItem key={p.id} value={p.id}>
                <p.Icon />
                <span className="font-mono text-sm">{p.name}</span>
                {credentialLayer(inspection, p.id) !== null && <CredsFound />}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {selected && <Hint>{selected.blurb}</Hint>}
        {selected && existingLayer && (
          <Hint>
            creds found — {selected.name} credentials are already stored in the{' '}
            {existingLayer === 'user' ? 'user config (~/.trau.ini)' : 'project config'}.
          </Hint>
        )}
      </div>

      {provider && (
        <div className="flex flex-col gap-4 rounded-md border border-border bg-secondary/20 p-4">
          {provider === 'linear' && (
            <>
              <SecretInput
                id="linear-key"
                label="linear api key"
                placeholder="lin_api_..."
                hasExisting={credentialLayer(inspection, 'linear') !== null}
                existingLayer={credentialLayer(inspection, 'linear') ?? undefined}
                value={linearKey}
                onChange={(v) => editCredential(v, setLinearKey)}
              />
              <Hint>Create one under Linear → Settings → API. Stored in the project config.</Hint>
            </>
          )}

          {provider === 'jira' && (
            <>
              <div className="flex flex-col gap-1.5">
                <FieldLabel htmlFor="jira-site">jira site url</FieldLabel>
                <TextInput
                  id="jira-site"
                  placeholder="https://acme.atlassian.net"
                  value={jiraSite}
                  onChange={(e) => editCredential(e.target.value, setJiraSite)}
                />
              </div>
              <div className="flex flex-col gap-1.5">
                <FieldLabel htmlFor="jira-email">account email</FieldLabel>
                <TextInput
                  id="jira-email"
                  placeholder="you@acme.com"
                  value={jiraEmail}
                  onChange={(e) => editCredential(e.target.value, setJiraEmail)}
                />
              </div>
              <SecretInput
                id="jira-token"
                label="jira api token"
                placeholder="ATATT..."
                hasExisting={credentialLayer(inspection, 'jira') !== null}
                existingLayer={credentialLayer(inspection, 'jira') ?? undefined}
                value={jiraToken}
                onChange={(v) => editCredential(v, setJiraToken)}
              />
            </>
          )}

          {provider === 'azure' && (
            <>
              <div className="flex flex-col gap-1.5">
                <FieldLabel htmlFor="azure-org">organization url</FieldLabel>
                <TextInput
                  id="azure-org"
                  placeholder="https://dev.azure.com/acme"
                  value={azureOrgUrl}
                  onChange={(e) => editCredential(e.target.value, setAzureOrgUrl)}
                />
              </div>
              <SecretInput
                id="azure-pat"
                label="azure personal access token"
                placeholder="Azure DevOps PAT"
                hasExisting={credentialLayer(inspection, 'azure') !== null}
                existingLayer={credentialLayer(inspection, 'azure') ?? undefined}
                value={azurePat}
                onChange={(v) => editCredential(v, setAzurePat)}
              />
              <Hint>
                The token needs the {AZURE_PAT_SCOPES} scopes.{' '}
                <a
                  href={AZURE_PAT_SETTINGS_URL}
                  target="_blank"
                  rel="noreferrer"
                  className="text-primary hover:underline"
                >
                  Mint one under User settings → Personal access tokens
                </a>
                .
              </Hint>
              <Hint>
                Work items are numbered uniquely across the organization, so trau addresses them
                by that number — work item 6694 is 6694 in identifiers, branch names and
                sentinels. There is no prefix to set.
              </Hint>
            </>
          )}

          {provider === 'internal' && (
            <Callout tone="success" title="No external tracker — ready to go">
              Issues live in trau's own store. Nothing to authenticate; the seed step is skipped.
            </Callout>
          )}

          {needsBinding && (
            <div className="flex flex-col gap-2">
              <FieldLabel htmlFor="tracker-binding">{BINDING_LABEL[provider]}</FieldLabel>
              <Select
                value={binding || undefined}
                onValueChange={chooseBinding}
                disabled={bindingOptions.length === 0}
              >
                <SelectTrigger id="tracker-binding" className="w-full">
                  <SelectValue
                    placeholder={
                      testState === 'ok'
                        ? 'Choose one'
                        : 'Test the connection to list them'
                    }
                  />
                </SelectTrigger>
                <SelectContent>
                  {bindingOptions.map((t) => (
                    <SelectItem key={t.key} value={t.key}>
                      {t.name}
                      {t.name === t.key ? '' : ` · ${t.key}`}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>

              <div className="flex flex-wrap items-center gap-3">
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => provider && test.mutate(provider)}
                  disabled={test.isPending || !canTest}
                >
                  <Plug className="size-4" />
                  {test.isPending ? 'Testing…' : 'Test connection'}
                </Button>
                {testState === 'ok' && (
                  <span className="font-mono text-xs text-done">
                    authenticated{bindingReachable ? ` — ${binding} reachable` : ''}
                  </span>
                )}
                {!canTest && (
                  <span className="font-sans text-xs text-muted-foreground">
                    {TEST_HINT[provider]}
                  </span>
                )}
              </div>

              {showUnreachable && (
                <MissingBinding provider={provider} binding={unreachableBinding} />
              )}

              {testState === 'fail' && (
                <Callout tone="fail" title="Connection failed">
                  {test.data?.error ?? 'The tracker rejected these credentials.'}
                  {test.data?.hint && (
                    <span className="mt-1 block text-muted-foreground">{test.data.hint}</span>
                  )}
                </Callout>
              )}
              {test.error && (
                <Callout tone="fail" title="Connection test failed">
                  {(test.error as Error).message}
                </Callout>
              )}

              {spec && canContinue && (
                <TrackerMappingSection
                  key={`${provider}:${binding}`}
                  provider={provider}
                  spec={spec}
                  probe={statusOptionsProbe(provider, fields, repo)}
                  onChange={setMapping}
                />
              )}
            </div>
          )}
        </div>
      )}

      {commit.error && (
        <Callout tone="fail" title="Couldn't save the tracker config">
          {(commit.error as Error).message}
        </Callout>
      )}

      <div className="flex items-center justify-between gap-3">
        <Button type="button" variant="ghost" onClick={onBack}>
          Back
        </Button>
        <div className="flex items-center gap-3">
          {needsBinding && testState !== 'ok' && (
            <span className="font-sans text-xs text-muted-foreground">
              Test the connection first.
            </span>
          )}
          <Button
            type="button"
            onClick={() => commit.mutate()}
            disabled={!canContinue || commit.isPending}
          >
            {commit.isPending ? 'Saving…' : 'Continue'}
            <ArrowRight className="size-4" />
          </Button>
        </div>
      </div>
    </div>
  )
}
