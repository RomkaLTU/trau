import { useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { Check, FolderGit2, FolderPlus, Plus, ShieldAlert } from 'lucide-react'

import { Button } from '@/components/ui/button'
import {
  browseQueryOptions,
  discoverRepos,
  gitInit,
  isRefusal,
  type DiscoverResponse,
} from '@/lib/fs-browse'
import { InspectError, type MemberRepo } from '@/lib/onboarding'
import { reposQueryOptions } from '@/lib/runs'
import { cn } from '@/lib/utils'
import { FolderPicker } from './folder-picker'
import { Callout, FieldLabel, Hint, RegistrationRefused, TextInput } from './ui'

export function StepPath({
  initialPath,
  members,
  projectName,
  onAdd,
  onRename,
}: {
  initialPath: string
  members: MemberRepo[]
  projectName: string
  onAdd: (paths: string[], groupName?: string) => Promise<void>
  onRename: (name: string) => Promise<void>
}) {
  const [path, setPath] = useState(initialPath)
  const [manual, setManual] = useState(initialPath !== '')
  const [scan, setScan] = useState<DiscoverResponse | null>(null)
  const [chosen, setChosen] = useState<string[]>([])
  const [edited, setEdited] = useState<string | null>(null)

  // Shares the picker's first query key; a refused browse has to hide the manual
  // escape hatch too, not just the picker.
  const roots = useQuery(browseQueryOptions(''))
  const registered = useQuery(reposQueryOptions)

  // A project forms at two members, so the name is asked for once this add would
  // reach that count.
  const taken = new Set(members.map((m) => m.root))
  const picking = scan && scan.children.length > 0 ? chosen.length : 1
  const grouping = members.length + picking > 1
  const name = edited ?? (projectName || scan?.name || members[0]?.repo || '')
  const groupName = grouping ? name : undefined
  const nameMissing = grouping && name.trim() === ''

  // A picked repo is taken straight on; anything else parks on its scan result,
  // which is either the repos below it or the offer to start one.
  const pick = useMutation({
    mutationFn: async (candidate: string) => {
      const found = await discoverRepos(candidate)
      if (!found.is_repo) return found
      await onAdd([found.path], groupName)
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
        await onAdd(chosen, groupName)
        return
      }
      const started = await gitInit(found.path)
      await onAdd([started.path], groupName)
    },
    onSuccess: () => setScan(null),
  })

  const quickPick = useMutation({
    mutationFn: (root: string) => onAdd([root], groupName),
  })

  // A folder repo is one Repo, so it joins whatever project the other members
  // already form rather than grouping its children into one of their own.
  const takeFolder = useMutation({
    mutationFn: (root: string) => onAdd([root], members.length > 0 ? name : undefined),
    onSuccess: () => setScan(null),
  })

  const rename = useMutation({ mutationFn: onRename })

  const err = pick.error ?? accept.error ?? quickPick.error ?? takeFolder.error
  const refused =
    isRefusal(roots.error) ||
    isRefusal(err) ||
    (err instanceof InspectError && err.refused)
  const pathError = err && !refused ? err.message : null
  const busy =
    pick.isPending || accept.isPending || quickPick.isPending || takeFolder.isPending
  const blocked = busy || nameMissing

  const quickPicks = (registered.data?.repos ?? []).filter((r) => !taken.has(r.root))
  const trimmed = path.trim()
  const canInspect = trimmed !== '' && !refused && !blocked

  // The button, the Enter key and the picker all land here, so the gate belongs
  // here and not on each control.
  function submit(candidate: string) {
    if (candidate === '' || blocked) return
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

      {!refused && grouping && (
        <div className="flex flex-col gap-1.5">
          <FieldLabel htmlFor="project-name">project name</FieldLabel>
          <TextInput
            id="project-name"
            value={name}
            invalid={nameMissing}
            placeholder="e.g. Acme Platform"
            onChange={(e) => setEdited(e.target.value)}
            onBlur={() => rename.mutate(name)}
          />
          <Hint>
            Two or more repos group under this name. Nothing moves on disk and every
            repo keeps its queue, runs, and history.
          </Hint>
          {rename.error && (
            <Callout tone="fail" title="That name can't be used">
              {rename.error.message}
            </Callout>
          )}
        </div>
      )}

      {!refused && scan && scan.children.length > 0 && (
        <div className="flex flex-col gap-3 rounded-md border border-border bg-card p-3">
          <div className="flex flex-col gap-1">
            <p className="font-mono text-sm text-foreground">
              <span className="font-mono">{scan.name}</span> holds{' '}
              {scan.truncated ? 'more than ' : ''}
              {scan.children.length} repositories
            </p>
            <Hint>
              It is not a repository itself. Take the whole folder as one repo, or add the
              ones you want — each of those joins the project as its own member.
            </Hint>
          </div>

          {scan.folder_repo && (
            <Callout
              tone="info"
              title={`Take ${scan.name} as one repo`}
              actions={
                <Button
                  type="button"
                  disabled={blocked}
                  onClick={() => takeFolder.mutate(scan.path)}
                >
                  <FolderGit2 className="size-4" />
                  {busy ? 'Adding…' : 'Register the folder'}
                </Button>
              }
            >
              One board, one queue and one Runs ledger for the whole folder. A run works
              across the repositories inside it and opens a pull request in each one it
              changed. Nothing on disk moves, and a repository you register separately
              stays independent.
            </Callout>
          )}
          <div className="flex items-center justify-between gap-2">
            <FieldLabel>
              {chosen.length} of {scan.children.length} selected
            </FieldLabel>
            <div className="flex items-center gap-1">
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => setChosen(scan.children.map((c) => c.path))}
              >
                Select all
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => setChosen([])}
              >
                Select none
              </Button>
            </div>
          </div>
          <ul className="max-h-64 divide-y divide-border/40 overflow-y-auto rounded-md border border-border">
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
              disabled={chosen.length === 0 || blocked}
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
              <Button
                type="button"
                disabled={blocked}
                onClick={() => accept.mutate(scan)}
              >
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
          <FolderPicker busy={busy} disabled={nameMissing} onSelect={submit} />
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

      {!refused && !scan && quickPicks.length > 0 && (
        <div className="flex flex-col gap-2">
          <FieldLabel>already registered</FieldLabel>
          <ul className="max-h-48 divide-y divide-border/40 overflow-y-auto rounded-md border border-border">
            {quickPicks.map((repo) => (
              <li key={repo.root}>
                <button
                  type="button"
                  disabled={blocked}
                  onClick={() => quickPick.mutate(repo.root)}
                  className="flex w-full min-w-0 items-center gap-2 px-2.5 py-1.5 text-left hover:bg-muted/50 disabled:opacity-50"
                >
                  <FolderGit2
                    className="size-3.5 shrink-0 text-primary"
                    aria-hidden="true"
                  />
                  <span className="flex min-w-0 flex-1 flex-col">
                    <span className="truncate font-mono text-xs text-foreground">
                      {repo.name}
                    </span>
                    <span className="truncate font-mono text-[0.6rem] text-muted-foreground">
                      {repo.root}
                    </span>
                  </span>
                  <Plus
                    className="size-3.5 shrink-0 text-muted-foreground"
                    aria-hidden="true"
                  />
                </button>
              </li>
            ))}
          </ul>
          <Hint>Repos the hub already knows — picking one adds it without browsing.</Hint>
        </div>
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
