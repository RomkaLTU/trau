import { useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { FolderGit2, ShieldAlert } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { browseQueryOptions, isRefusal } from '@/lib/fs-browse'
import {
  InspectError,
  inspectRepo,
  registerForOnboarding,
  type RepoInspection,
} from '@/lib/onboarding'
import { FolderPicker } from './folder-picker'
import { Callout, FieldLabel, Hint, RegistrationRefused, TextInput } from './ui'

export function StepPath({
  initialPath,
  onInspected,
}: {
  initialPath: string
  onInspected: (inspection: RepoInspection, repo: string) => void
}) {
  const [path, setPath] = useState(initialPath)
  const [manual, setManual] = useState(initialPath !== '')

  // Shares the picker's first query key; a refused browse has to hide the manual
  // escape hatch too, not just the picker.
  const roots = useQuery(browseQueryOptions(''))

  const inspect = useMutation({
    mutationFn: async (p: string) => {
      const inspection = await inspectRepo(p)
      const repo = await registerForOnboarding(p)
      return { inspection, repo: repo.name }
    },
    onSuccess: ({ inspection, repo }) => onInspected(inspection, repo),
  })

  const err = inspect.error
  const refused = isRefusal(roots.error) || (err instanceof InspectError && err.refused)
  const pathError = err && !refused ? err.message : null

  const trimmed = path.trim()
  const canInspect = trimmed !== '' && !refused && !inspect.isPending

  function submit(candidate: string) {
    if (candidate === '') return
    setPath(candidate)
    inspect.mutate(candidate)
  }

  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-col gap-1.5">
        <h2 className="font-mono text-base text-foreground">Where does the repo live?</h2>
        <Hint>
          Pick a git repository from this machine's folders. trau inspects it in place —
          nothing is written until you pick a tracker.
        </Hint>
      </div>

      {refused && <RegistrationRefused />}

      {!refused &&
        (manual ? (
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
                  if (e.key === 'Enter' && !e.nativeEvent.isComposing) submit(trimmed)
                }}
              />
              <Button type="button" onClick={() => submit(trimmed)} disabled={!canInspect}>
                <FolderGit2 className="size-4" />
                {inspect.isPending ? 'Inspecting…' : 'Inspect repo'}
              </Button>
            </div>
          </div>
        ) : (
          <FolderPicker busy={inspect.isPending} onSelect={submit} />
        ))}

      {!refused && (
        <button
          type="button"
          onClick={() => setManual(!manual)}
          className="self-start font-mono text-[0.65rem] text-muted-foreground underline underline-offset-2 hover:text-foreground"
        >
          {manual ? 'browse folders instead' : 'enter a path manually'}
        </button>
      )}

      {pathError && (
        <Callout tone="fail" title="That path can't be used">
          {pathError}
        </Callout>
      )}

      {!refused && (
        <div className="flex items-start gap-2.5 rounded-md border border-info/40 bg-info/5 px-3 py-3">
          <ShieldAlert className="mt-0.5 size-3.5 shrink-0 text-info" aria-hidden="true" />
          <Hint>
            Browsing and registration are open on a loopback hub, or when the operator sets{' '}
            <span className="font-mono">SERVE_ALLOW_REGISTER</span>. The repo becomes
            startable from the hub with no serve restart.
          </Hint>
        </div>
      )}
    </div>
  )
}
