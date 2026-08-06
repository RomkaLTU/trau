import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { ArrowRight, ExternalLink } from 'lucide-react'

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
  BITBUCKET_TOKEN_URL,
  ensureGitignore,
  essentialsConfigWrites,
  essentialsProjectKeys,
  FORGE_NAMES,
  forgeLabel,
  type EssentialsFields,
  type MemberRepo,
} from '@/lib/onboarding'
import { writeProjectTracker } from '@/lib/projects'
import { FieldLabel, Hint, SecretInput, TextInput, Toggle } from './ui'

// Radix rejects an empty item value, so the "let trau read it" choice carries a
// sentinel that never leaves this file.
const FORGE_AUTO = 'auto'

const FORGE_HINT =
  "Read from the remote — name it only when trau couldn't. A self-hosted GitHub Enterprise install is GitHub, and picking it is what unblocks delivery. Naming another forge records the truth for doctor and the sweep, but trau opens pull requests on GitHub and Bitbucket Cloud only."

function detectedBranch(member: MemberRepo): string {
  return member.inspection.default_branch || 'main'
}

function detectedForge(member: MemberRepo): string {
  return FORGE_NAMES.includes(member.inspection.forge) ? member.inspection.forge : ''
}

function ForgeSelect({
  id,
  value,
  onChange,
}: {
  id: string
  value: string
  onChange: (value: string) => void
}) {
  return (
    <Select value={value || FORGE_AUTO} onValueChange={onChange}>
      <SelectTrigger id={id} className="w-full">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={FORGE_AUTO}>identify from the remote</SelectItem>
        {FORGE_NAMES.map((name) => (
          <SelectItem key={name} value={name}>
            {forgeLabel(name)}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

export function StepEssentials({
  members,
  project,
  onBack,
  onContinue,
}: {
  members: MemberRepo[]
  project: string | null
  onBack: () => void
  onContinue: (fields: EssentialsFields) => void
}) {
  const primary = members[0]
  const prefill = primary.inspection.prefill
  // A Folder repo is not a git repository, so a .gitignore in it is inert clutter.
  const gitignorable = members.filter((m) => m.inspection.kind !== 'folder')
  // A Folder root has no remote of its own, and a child never inherits its FORGE.
  const forgeable = members.filter((m) => m.inspection.kind !== 'folder')
  const [branches, setBranches] = useState<Record<string, string>>(() =>
    Object.fromEntries(members.map((m) => [m.root, detectedBranch(m)])),
  )
  const [forges, setForges] = useState<Record<string, string>>(() =>
    Object.fromEntries(forgeable.map((m) => [m.root, detectedForge(m)])),
  )
  const [readyLabel, setReadyLabel] = useState(prefill?.ready_label ?? 'ready-for-agent')
  const [epicFlow, setEpicFlow] = useState(prefill?.epic_flow ?? false)
  const [gitignore, setGitignore] = useState(true)
  const [bitbucketEmail, setBitbucketEmail] = useState('')
  const [bitbucketToken, setBitbucketToken] = useState('')

  // The forge a member would actually ship through: the pick when there is one,
  // the detected remote otherwise.
  const bitbucketRoots = forgeable
    .filter((m) => (forges[m.root] || detectedForge(m)) === 'bitbucket')
    .map((m) => m.root)

  const fields: EssentialsFields = {
    baseBranches: members.map((m) => ({
      repo: m.repo,
      root: m.root,
      branch: branches[m.root],
    })),
    // A pick that only restates what the remote already answered is noise in .trau.ini.
    forges: forgeable
      .filter((m) => forges[m.root] !== detectedForge(m))
      .map((m) => ({ root: m.root, forge: forges[m.root] })),
    bitbucket: { roots: bitbucketRoots, email: bitbucketEmail, token: bitbucketToken },
    readyLabel,
    epicFlow,
  }

  function setBranch(root: string, value: string) {
    setBranches((prev) => ({ ...prev, [root]: value }))
  }

  function setForge(root: string, value: string) {
    setForges((prev) => ({ ...prev, [root]: value === FORGE_AUTO ? '' : value }))
  }

  // Every field has a working default, so this advances on settle, not just success.
  // The writes are independent: a member the hub rejects must not take the ones
  // queued behind it down with it.
  const commit = useMutation({
    mutationFn: async () => {
      const pending: (() => Promise<unknown>)[] = []
      if (project !== null) {
        pending.push(() => writeProjectTracker(project, essentialsProjectKeys(fields)))
      }
      pending.push(
        ...essentialsConfigWrites(fields, project).map(
          ({ root, ...write }) => () => writeConfig(root, write),
        ),
      )
      if (gitignore) {
        pending.push(...gitignorable.map((m) => () => ensureGitignore(m.root)))
      }
      const failures: unknown[] = []
      for (const write of pending) {
        try {
          await write()
        } catch (err) {
          failures.push(err)
        }
      }
      if (failures.length > 0) throw failures[0]
    },
    onSettled: () => onContinue(fields),
  })

  // Only a repo detection could not place has to be named; every other member may
  // be left to the remote.
  const canContinue =
    fields.baseBranches.every((b) => b.branch.trim() !== '') &&
    readyLabel.trim() !== '' &&
    forgeable.every((m) => m.inspection.forge !== 'unknown' || forges[m.root] !== '')

  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-col gap-1.5">
        <h2 className="font-mono text-base text-foreground">The essentials</h2>
        <Hint>Sensible defaults are filled in — tweak only what you need, then carry on.</Hint>
      </div>

      {members.length > 1 ? (
        <div className="flex flex-col gap-3">
          <FieldLabel>base branch</FieldLabel>
          {members.map((m, i) => (
            <div key={m.root} className="flex flex-col gap-1.5">
              <label
                htmlFor={`base-branch-${i}`}
                className="font-mono text-xs text-muted-foreground"
              >
                {m.repo}
              </label>
              <TextInput
                id={`base-branch-${i}`}
                value={branches[m.root]}
                onChange={(e) => setBranch(m.root, e.target.value)}
              />
            </div>
          ))}
          <Hint>Each repo keeps its own base branch, prefilled from the one detected in it.</Hint>
        </div>
      ) : (
        <div className="flex flex-col gap-2">
          <FieldLabel htmlFor="base-branch">base branch</FieldLabel>
          <TextInput
            id="base-branch"
            value={branches[primary.root]}
            onChange={(e) => setBranch(primary.root, e.target.value)}
          />
          <Hint>Detected default branch: {detectedBranch(primary)}.</Hint>
        </div>
      )}

      {forgeable.length > 0 &&
        (forgeable.length > 1 ? (
          <div className="flex flex-col gap-3">
            <FieldLabel>forge</FieldLabel>
            {forgeable.map((m, i) => (
              <div key={m.root} className="flex flex-col gap-1.5">
                <label htmlFor={`forge-${i}`} className="font-mono text-xs text-muted-foreground">
                  {m.repo}
                </label>
                <ForgeSelect
                  id={`forge-${i}`}
                  value={forges[m.root]}
                  onChange={(v) => setForge(m.root, v)}
                />
              </div>
            ))}
            <Hint>{FORGE_HINT}</Hint>
          </div>
        ) : (
          <div className="flex flex-col gap-2">
            <FieldLabel htmlFor="forge">forge</FieldLabel>
            <ForgeSelect
              id="forge"
              value={forges[forgeable[0].root]}
              onChange={(v) => setForge(forgeable[0].root, v)}
            />
            <Hint>{FORGE_HINT}</Hint>
          </div>
        ))}

      {bitbucketRoots.length > 0 && (
        <div className="flex flex-col gap-3">
          <div className="flex flex-col gap-2">
            <FieldLabel htmlFor="bitbucket-email">bitbucket email</FieldLabel>
            <TextInput
              id="bitbucket-email"
              value={bitbucketEmail}
              placeholder="you@acme.com"
              onChange={(e) => setBitbucketEmail(e.target.value)}
            />
          </div>
          <SecretInput
            id="bitbucket-api-token"
            label="bitbucket api token"
            placeholder="Atlassian API token"
            value={bitbucketToken}
            onChange={setBitbucketToken}
          />
          <Hint>
            Bitbucket Cloud has no CLI to hold a session, so delivery authenticates with your
            Atlassian account: both keys are required to open a pull request there. Mint a token
            with pull-request scopes at{' '}
            <span className="font-mono break-all">{BITBUCKET_TOKEN_URL}</span>. They are stored in
            your user config, so every Bitbucket repo on this machine shares them.
          </Hint>
        </div>
      )}

      <div className="flex flex-col gap-2">
        <FieldLabel htmlFor="ready-label">ready label</FieldLabel>
        <TextInput
          id="ready-label"
          value={readyLabel}
          onChange={(e) => setReadyLabel(e.target.value)}
        />
        <Hint>Only tickets carrying this label are picked up by the loop.</Hint>
      </div>

      <Toggle
        id="epic-flow"
        checked={epicFlow}
        onChange={setEpicFlow}
        label="epic flow"
        description="Stack an epic's sub-issues on a shared integration branch instead of one PR each."
      />

      {gitignorable.length > 0 && (
        <Toggle
          id="gitignore"
          checked={gitignore}
          onChange={setGitignore}
          label="add .trau/ to .gitignore"
          description={
            gitignorable.length > 1
              ? 'Keeps the local run store and generated config out of version control in every member repo.'
              : 'Keeps the local run store and generated config out of version control.'
          }
        />
      )}

      <a
        href="/settings"
        className="inline-flex w-fit items-center gap-1.5 font-mono text-xs text-muted-foreground transition-colors hover:text-foreground"
      >
        <ExternalLink className="size-3.5" aria-hidden="true" />
        Need more knobs? Open Settings.
      </a>

      <div className="flex items-center justify-between">
        <Button type="button" variant="ghost" onClick={onBack}>
          Back
        </Button>
        <Button type="button" onClick={() => commit.mutate()} disabled={!canContinue || commit.isPending}>
          {commit.isPending ? 'Saving…' : 'Save & continue'}
          <ArrowRight className="size-4" />
        </Button>
      </div>
    </div>
  )
}
