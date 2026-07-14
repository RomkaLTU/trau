import { useMemo, useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { ArrowRight, Plug } from 'lucide-react'

import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { writeConfig } from '@/lib/config'
import {
  credentialLayer,
  preselectProvider,
  testTracker,
  trackerCanContinue,
  trackerConfigWrites,
  type RepoInspection,
  type Team,
  type TestState,
  type TrackerFields,
  type TrackerProvider,
} from '@/lib/onboarding'
import { cn } from '@/lib/utils'
import { Callout, FieldLabel, Hint, SecretInput, TextInput } from './ui'

const PROVIDERS: { id: TrackerProvider; name: string; blurb: string }[] = [
  { id: 'linear', name: 'Linear', blurb: 'Sync issues from a Linear team. Needs an API key.' },
  { id: 'jira', name: 'Jira', blurb: 'Sync from a Jira project. Needs a site URL, email + token.' },
  { id: 'internal', name: 'Internal', blurb: "No external tracker — issues live in trau's own store." },
]

export function StepTracker({
  inspection,
  repo,
  onBack,
  onContinue,
}: {
  inspection: RepoInspection
  repo: string
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
  const [binding, setBinding] = useState(inspection.prefill?.team ?? '')

  const fields: TrackerFields = {
    linearKey,
    jiraBaseUrl: jiraSite,
    jiraEmail,
    jiraToken,
    binding,
  }

  const test = useMutation({
    mutationFn: (p: TrackerProvider) =>
      testTracker(p, {
        repo,
        api_key: linearKey.trim() || undefined,
        base_url: jiraSite.trim() || undefined,
        email: jiraEmail.trim() || undefined,
        api_token: jiraToken.trim() || undefined,
      }),
    onSuccess: (res) => {
      const first = res.teams?.[0]
      if (binding === '' && first) setBinding(first.key)
    },
  })

  const testState: TestState = test.isPending
    ? 'testing'
    : test.data
      ? test.data.ok
        ? 'ok'
        : 'fail'
      : 'idle'

  const bindingOptions = useMemo<Team[]>(() => {
    const teams = test.data?.ok ? (test.data.teams ?? []) : []
    if (teams.length > 0) return teams
    return binding !== '' ? [{ key: binding, name: binding }] : []
  }, [test.data, binding])

  const commit = useMutation({
    mutationFn: async () => {
      if (!provider) return
      for (const w of trackerConfigWrites(provider, fields)) {
        await writeConfig(repo, w)
      }
    },
    onSuccess: () => provider && onContinue(provider, fields),
  })

  const needsBinding = provider === 'linear' || provider === 'jira'
  const canContinue = trackerCanContinue(provider, binding, testState)

  function choose(next: TrackerProvider) {
    if (next === provider) return
    setProvider(next)
    setBinding(next === inspection.prefill?.provider ? (inspection.prefill?.team ?? '') : '')
    test.reset()
  }

  function editCredential(value: string, setter: (v: string) => void) {
    setter(value)
    if (testState !== 'idle') test.reset()
  }

  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-col gap-1.5">
        <h2 className="font-mono text-base text-foreground">Where do the tickets come from?</h2>
        <Hint>
          trau writes <span className="font-mono">TRACKER_PROVIDER</span> explicitly and tests the
          connection before it will sync.
        </Hint>
      </div>

      <div role="radiogroup" aria-label="Tracker provider" className="flex flex-col gap-2 sm:flex-row">
        {PROVIDERS.map((p) => {
          const active = provider === p.id
          const suggested =
            p.id !== 'internal' && credentialLayer(inspection, p.id) !== null && !active
          return (
            <button
              key={p.id}
              type="button"
              role="radio"
              aria-checked={active}
              onClick={() => choose(p.id)}
              className={cn(
                'flex flex-1 flex-col gap-1.5 rounded-md border p-3 text-left transition-colors',
                active
                  ? 'border-primary/60 bg-primary/5'
                  : 'border-border bg-secondary/20 hover:border-ring/40',
              )}
            >
              <span className="flex items-center gap-2 font-mono text-sm text-foreground">
                <span aria-hidden="true" className={active ? 'text-primary' : 'text-muted-foreground'}>
                  {active ? '●' : '○'}
                </span>
                {p.name}
                {suggested && (
                  <span className="rounded-full border border-teal/50 bg-teal/10 px-1.5 py-0.5 font-mono text-[0.6rem] text-teal">
                    creds found
                  </span>
                )}
              </span>
              <span className="font-sans text-xs leading-relaxed text-muted-foreground">
                {p.blurb}
              </span>
            </button>
          )
        })}
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

          {provider === 'internal' && (
            <Callout tone="success" title="No external tracker — ready to go">
              Issues live in trau's own store. Nothing to authenticate; the seed step is skipped.
            </Callout>
          )}

          {needsBinding && (
            <div className="flex flex-col gap-2">
              <FieldLabel htmlFor="tracker-binding">
                {provider === 'jira' ? 'jira project' : 'linear team'}
              </FieldLabel>
              <Select
                value={binding || undefined}
                onValueChange={setBinding}
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
                  disabled={test.isPending}
                >
                  <Plug className="size-4" />
                  {test.isPending ? 'Testing…' : 'Test connection'}
                </Button>
                {testState === 'ok' && (
                  <span className="font-mono text-xs text-done">
                    authenticated{binding ? ` — ${binding} reachable` : ''}
                  </span>
                )}
              </div>

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
