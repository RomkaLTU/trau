import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { FolderGit2, ShieldAlert } from 'lucide-react'

import { Button } from '@/components/ui/button'
import {
  InspectError,
  inspectRepo,
  registerForOnboarding,
  type RepoInspection,
} from '@/lib/onboarding'
import { Callout, FieldLabel, Hint, TextInput } from './ui'

export function StepPath({
  initialPath,
  onInspected,
}: {
  initialPath: string
  onInspected: (inspection: RepoInspection, repo: string) => void
}) {
  const [path, setPath] = useState(initialPath)

  const inspect = useMutation({
    mutationFn: async (p: string) => {
      const inspection = await inspectRepo(p)
      const repo = await registerForOnboarding(p)
      return { inspection, repo: repo.name }
    },
    onSuccess: ({ inspection, repo }) => onInspected(inspection, repo),
  })

  const err = inspect.error
  const refused = err instanceof InspectError && err.refused
  const pathError = err && !refused ? err.message : null

  const trimmed = path.trim()
  const canInspect = trimmed !== '' && !refused && !inspect.isPending

  function submit() {
    if (trimmed === '') return
    inspect.mutate(trimmed)
  }

  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-col gap-1.5">
        <h2 className="font-mono text-base text-foreground">Where does the repo live?</h2>
        <Hint>
          Give the absolute path to a git repository on this machine. trau inspects it in place
          — nothing is written until you pick a tracker.
        </Hint>
      </div>

      {refused && (
        <Callout tone="fail" title="Registration is disabled on this hub">
          This <span className="font-mono">trau serve</span> is bound to a routable address, so
          registering a repo needs <span className="font-mono">SERVE_ALLOW_REGISTER=1</span> on
          top of <span className="font-mono">SERVE_TOKEN</span>. Set it to open registration
          deliberately, or run onboarding from a loopback hub on the host.
        </Callout>
      )}

      <div className="flex flex-col gap-2">
        <FieldLabel htmlFor="repo-path">repo path</FieldLabel>
        <div className="flex items-center gap-2">
          <TextInput
            id="repo-path"
            value={path}
            invalid={pathError !== null}
            placeholder="/Users/you/Projects/acme"
            onChange={(e) => setPath(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && !e.nativeEvent.isComposing) submit()
            }}
          />
          <Button type="button" onClick={submit} disabled={!canInspect}>
            <FolderGit2 className="size-4" />
            {inspect.isPending ? 'Inspecting…' : 'Inspect repo'}
          </Button>
        </div>
        {pathError && (
          <Callout tone="fail" title="That path can't be used">
            {pathError}
          </Callout>
        )}
      </div>

      {!refused && (
        <div className="flex items-start gap-2.5 rounded-md border border-info/40 bg-info/5 px-3 py-3">
          <ShieldAlert className="mt-0.5 size-3.5 shrink-0 text-info" aria-hidden="true" />
          <Hint>
            Registration is open on a loopback hub, or when the operator sets{' '}
            <span className="font-mono">SERVE_ALLOW_REGISTER</span>. The repo becomes startable
            from the hub with no serve restart.
          </Hint>
        </div>
      )}
    </div>
  )
}
