import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { ArrowRight, ExternalLink } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { writeConfig } from '@/lib/config'
import {
  ensureGitignore,
  essentialsConfigWrites,
  type EssentialsFields,
  type MemberRepo,
} from '@/lib/onboarding'
import { FieldLabel, Hint, TextInput, Toggle } from './ui'

function detectedBranch(member: MemberRepo): string {
  return member.inspection.default_branch || 'main'
}

export function StepEssentials({
  members,
  onBack,
  onContinue,
}: {
  members: MemberRepo[]
  onBack: () => void
  onContinue: (fields: EssentialsFields) => void
}) {
  const primary = members[0]
  const prefill = primary.inspection.prefill
  const [branches, setBranches] = useState<Record<string, string>>(() =>
    Object.fromEntries(members.map((m) => [m.root, detectedBranch(m)])),
  )
  const [readyLabel, setReadyLabel] = useState(prefill?.ready_label ?? 'ready-for-agent')
  const [epicFlow, setEpicFlow] = useState(prefill?.epic_flow ?? false)
  const [gitignore, setGitignore] = useState(true)

  const fields: EssentialsFields = {
    baseBranches: members.map((m) => ({
      repo: m.repo,
      root: m.root,
      branch: branches[m.root],
    })),
    readyLabel,
    epicFlow,
  }

  function setBranch(root: string, value: string) {
    setBranches((prev) => ({ ...prev, [root]: value }))
  }

  // Every field has a working default, so this advances on settle, not just success.
  // The writes are independent: a member the hub rejects must not take the ones
  // queued behind it down with it.
  const commit = useMutation({
    mutationFn: async () => {
      const pending: (() => Promise<unknown>)[] = essentialsConfigWrites(fields).map(
        ({ root, ...write }) => () => writeConfig(root, write),
      )
      if (gitignore) {
        pending.push(...members.map((m) => () => ensureGitignore(m.root)))
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

  const canContinue =
    fields.baseBranches.every((b) => b.branch.trim() !== '') && readyLabel.trim() !== ''

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

      <Toggle
        id="gitignore"
        checked={gitignore}
        onChange={setGitignore}
        label="add .trau/ to .gitignore"
        description={
          members.length > 1
            ? 'Keeps the local run store and generated config out of version control in every member repo.'
            : 'Keeps the local run store and generated config out of version control.'
        }
      />

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
