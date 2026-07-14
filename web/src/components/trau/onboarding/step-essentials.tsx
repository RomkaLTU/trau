import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { ArrowRight, ExternalLink } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { writeConfig } from '@/lib/config'
import {
  ensureGitignore,
  essentialsConfigWrites,
  type EssentialsFields,
  type RepoInspection,
} from '@/lib/onboarding'
import { FieldLabel, Hint, TextInput, Toggle } from './ui'

export function StepEssentials({
  inspection,
  repo,
  onBack,
  onContinue,
}: {
  inspection: RepoInspection
  repo: string
  onBack: () => void
  onContinue: (fields: EssentialsFields) => void
}) {
  const [baseBranch, setBaseBranch] = useState(inspection.default_branch || 'main')
  const [readyLabel, setReadyLabel] = useState(
    inspection.prefill?.ready_label ?? 'ready-for-agent',
  )
  const [epicFlow, setEpicFlow] = useState(inspection.prefill?.epic_flow ?? false)
  const [gitignore, setGitignore] = useState(true)

  const fields: EssentialsFields = { baseBranch, readyLabel, epicFlow }

  // Every field has a working default, so this advances on settle, not just success.
  const commit = useMutation({
    mutationFn: async () => {
      for (const w of essentialsConfigWrites(fields)) {
        await writeConfig(repo, w)
      }
      if (gitignore) await ensureGitignore(repo)
    },
    onSettled: () => onContinue(fields),
  })

  const canContinue = baseBranch.trim() !== '' && readyLabel.trim() !== ''

  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-col gap-1.5">
        <h2 className="font-mono text-base text-foreground">The essentials</h2>
        <Hint>Sensible defaults are filled in — tweak only what you need, then seed the backlog.</Hint>
      </div>

      <div className="flex flex-col gap-2">
        <FieldLabel htmlFor="base-branch">base branch</FieldLabel>
        <TextInput
          id="base-branch"
          value={baseBranch}
          onChange={(e) => setBaseBranch(e.target.value)}
        />
        <Hint>Detected default branch: {inspection.default_branch || 'main'}.</Hint>
      </div>

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
        description="Keeps the local run store and generated config out of version control."
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
          {commit.isPending ? 'Saving…' : 'Save & seed backlog'}
          <ArrowRight className="size-4" />
        </Button>
      </div>
    </div>
  )
}
