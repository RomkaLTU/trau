import { useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { Check, FolderGit2, FolderPlus, ShieldAlert } from 'lucide-react'

import { Button } from '@/components/ui/button'
import {
  browseQueryOptions,
  discoverRepos,
  gitInit,
  isRefusal,
  type DiscoverResponse,
} from '@/lib/fs-browse'
import { InspectError, type MemberRepo } from '@/lib/onboarding'
import { cn } from '@/lib/utils'
import { FolderPicker } from './folder-picker'
import { Callout, FieldLabel, Hint, RegistrationRefused, TextInput } from './ui'

export function StepPath({
  initialPath,
  members,
  onAdd,
}: {
  initialPath: string
  members: MemberRepo[]
  onAdd: (paths: string[], groupName?: string) => Promise<void>
}) {
  const [path, setPath] = useState(initialPath)
  const [manual, setManual] = useState(initialPath !== '')
  const [scan, setScan] = useState<DiscoverResponse | null>(null)
  const [chosen, setChosen] = useState<string[]>([])

  // Shares the picker's first query key; a refused browse has to hide the manual
  // escape hatch too, not just the picker.
  const roots = useQuery(browseQueryOptions(''))

  // A picked repo is taken straight on; anything else parks on its scan result,
  // which is either the repos below it or the offer to start one.
  const pick = useMutation({
    mutationFn: async (candidate: string) => {
      const found = await discoverRepos(candidate)
      if (!found.is_repo) return found
      await onAdd([found.path])
      return null
    },
    onSuccess: (found) => {
      setScan(found)
      setChosen(found?.children.map((c) => c.path) ?? [])
    },
  })

  const accept = useMutation({
    mutationFn: async (found: DiscoverResponse) => {
      if (found.children.length > 0) {
        await onAdd(chosen, found.name)
        return
      }
      const started = await gitInit(found.path)
      await onAdd([started.path])
    },
    onSuccess: () => setScan(null),
  })

  const err = pick.error ?? accept.error
  const refused =
    isRefusal(roots.error) ||
    isRefusal(err) ||
    (err instanceof InspectError && err.refused)
  const pathError = err && !refused ? err.message : null
  const busy = pick.isPending || accept.isPending

  const trimmed = path.trim()
  const canInspect = trimmed !== '' && !refused && !busy

  function submit(candidate: string) {
    if (candidate === '') return
    setPath(candidate)
    pick.mutate(candidate)
  }

  function toggle(candidate: string) {
    setChosen((prev) =>
      prev.includes(candidate)
        ? prev.filter((p) => p !== candidate)
        : [...prev, candidate],
    )
  }

  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-col gap-1.5">
        <h2 className="font-mono text-base text-foreground">
          {members.length === 0
            ? 'Where does the repo live?'
            : 'Add another repo to the project'}
        </h2>
        <Hint>
          Pick a folder from this machine. trau inspects it in place — nothing is written
          until you pick a tracker.
        </Hint>
      </div>

      {members.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5">
          <FieldLabel>added</FieldLabel>
          {members.map((member) => (
            <span
              key={member.root}
              className="rounded border border-done/40 bg-done/5 px-1.5 py-0.5 font-mono text-[0.65rem] text-foreground"
            >
              {member.repo}
            </span>
          ))}
        </div>
      )}

      {refused && <RegistrationRefused />}

      {!refused && scan && scan.children.length > 0 && (
        <div className="flex flex-col gap-3 rounded-md border border-border bg-card p-3">
          <div className="flex flex-col gap-1">
            <p className="font-mono text-sm text-foreground">
              <span className="font-mono">{scan.name}</span> holds {scan.children.length}{' '}
              repositories
            </p>
            <Hint>
              It is not a repository itself. Add the ones you want — each joins the project
              as its own member.
            </Hint>
          </div>
          <ul className="divide-y divide-border/40 overflow-hidden rounded-md border border-border">
            {scan.children.map((child) => {
              const on = chosen.includes(child.path)
              return (
                <li key={child.path}>
                  <button
                    type="button"
                    role="checkbox"
                    aria-checked={on}
                    onClick={() => toggle(child.path)}
                    className="flex w-full items-center gap-2 px-2.5 py-1.5 text-left hover:bg-muted/50"
                  >
                    <span
                      aria-hidden="true"
                      className={cn(
                        'flex size-4 shrink-0 items-center justify-center rounded-[3px] border',
                        on ? 'border-primary bg-primary/20 text-primary' : 'border-border',
                      )}
                    >
                      {on && <Check className="size-3" />}
                    </span>
                    <FolderGit2
                      className="size-3.5 shrink-0 text-primary"
                      aria-hidden="true"
                    />
                    <span className="truncate font-mono text-xs text-foreground">
                      {child.name}
                    </span>
                  </button>
                </li>
              )
            })}
          </ul>
          <div className="flex items-center justify-between">
            <Button type="button" variant="ghost" onClick={() => setScan(null)}>
              Back to folders
            </Button>
            <Button
              type="button"
              disabled={chosen.length === 0 || busy}
              onClick={() => accept.mutate(scan)}
            >
              <FolderGit2 className="size-4" />
              {busy ? 'Adding…' : `Add ${chosen.length} to the project`}
            </Button>
          </div>
        </div>
      )}

      {!refused && scan && scan.children.length === 0 && (
        <Callout
          tone="info"
          title={`${scan.name} has no git repository — start one?`}
          actions={
            <>
              <Button type="button" disabled={busy} onClick={() => accept.mutate(scan)}>
                <FolderPlus className="size-4" />
                {busy ? 'Initializing…' : 'Initialize git here'}
              </Button>
              <Button type="button" variant="ghost" onClick={() => setScan(null)}>
                Back to folders
              </Button>
            </>
          }
        >
          trau runs <span className="font-mono">git init</span> in{' '}
          <span className="font-mono">{scan.path}</span> and commits an empty first commit so
          the repo has a base branch. Nothing else is touched, and nothing happens if you go
          back.
        </Callout>
      )}

      {!refused &&
        !scan &&
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
                {busy ? 'Inspecting…' : 'Inspect repo'}
              </Button>
            </div>
          </div>
        ) : (
          <FolderPicker busy={busy} onSelect={submit} />
        ))}

      {!refused && !scan && (
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
