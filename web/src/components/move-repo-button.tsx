import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { FolderInput, Loader2 } from 'lucide-react'

import { ConfirmDialog } from '@/components/trau/confirm-dialog'
import { Input } from '@/components/ui/input'
import type { RepoView } from '@/lib/instances'
import {
  deleteProject,
  moveRepo,
  otherProjects,
  projectHolding,
  projectsQueryOptions,
  type MoveTarget,
  type ProjectView,
} from '@/lib/projects'
import { cn } from '@/lib/utils'

// Joining is the hub's own move — the repo leaves its old project and that
// project goes if it was the last member. Leaving keeps the emptied project, so
// the outcome offers to delete it.
export function MoveRepoButton({ repo }: { repo: RepoView }) {
  const queryClient = useQueryClient()
  const { data } = useQuery(projectsQueryOptions)
  const projects = data?.projects ?? []
  const holder = projectHolding(repo.root, projects)

  const [picking, setPicking] = useState(false)
  const [target, setTarget] = useState<MoveTarget | null>(null)
  const [emptied, setEmptied] = useState<ProjectView | null>(null)

  const move = useMutation({
    mutationFn: (chosen: MoveTarget) => moveRepo(repo.root, chosen),
    onSuccess: ({ project, emptied: husk }) => {
      toast.success(
        project
          ? `${repo.name} moved to ${project.name}`
          : `${repo.name} left ${holder?.name ?? 'its project'}`,
      )
      setEmptied(husk)
      void queryClient.invalidateQueries({ queryKey: ['projects'] })
      void queryClient.invalidateQueries({ queryKey: ['repos'] })
    },
    onError: (err: Error) => toast.error(err.message),
  })

  const remove = useMutation({
    mutationFn: (id: string) => deleteProject(id),
    onSuccess: (project) => {
      toast.success(`${project.name} deleted`)
      void queryClient.invalidateQueries({ queryKey: ['projects'] })
    },
    onError: (err: Error) => toast.error(err.message),
    onSettled: () => setEmptied(null),
  })

  function open() {
    setTarget(null)
    setPicking(true)
  }

  return (
    <>
      <button
        type="button"
        onClick={open}
        disabled={move.isPending}
        title="Move this repo to another project"
        className="inline-flex w-fit shrink-0 items-center gap-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground disabled:opacity-50 disabled:hover:text-muted-foreground"
      >
        {move.isPending ? (
          <Loader2 className="size-3.5 animate-spin" />
        ) : (
          <FolderInput className="size-3.5" />
        )}
        {move.isPending ? 'Moving…' : 'Move…'}
      </button>
      <ConfirmDialog
        open={picking}
        onOpenChange={setPicking}
        windowTitle="move repo"
        title={`Move ${repo.name} to…`}
        description={
          <MoveTargets
            repo={repo}
            holder={holder}
            projects={projects}
            target={target}
            onPick={setTarget}
          />
        }
        confirmLabel="Move"
        confirmDisabled={target === null || unnamed(target)}
        onConfirm={() => target && move.mutate(target)}
      />
      {emptied && (
        <ConfirmDialog
          open
          onOpenChange={() => setEmptied(null)}
          windowTitle="delete project"
          title={`${emptied.name} is empty — delete it?`}
          description="Deleting drops the grouping only. Every repo that was in it stays on the hub, in no project."
          confirmLabel="Delete project"
          cancelLabel="Keep it"
          destructive
          onConfirm={() => remove.mutate(emptied.id)}
        />
      )}
    </>
  )
}

function unnamed(target: MoveTarget): boolean {
  return target.kind === 'new' && target.name.trim() === ''
}

function MoveTargets({
  repo,
  holder,
  projects,
  target,
  onPick,
}: {
  repo: RepoView
  holder: ProjectView | null
  projects: readonly ProjectView[]
  target: MoveTarget | null
  onPick: (target: MoveTarget) => void
}) {
  // The last member out empties the project it leaves: joining another prunes it
  // on the way, leaving every project keeps it for the delete prompt.
  const empties = holder !== null && holder.repos.length === 1
  return (
    <>
      {holder
        ? `${repo.name} is in ${holder.name}.`
        : `${repo.name} is in no project.`}{' '}
      Its runs and tracker seeding follow the project it lands in — nothing on
      disk moves.
      <span className="mt-3 flex flex-col gap-1">
        {otherProjects(repo.root, projects).map((project) => (
          <TargetOption
            key={project.id}
            label={project.name}
            hint={`${project.repos.length} ${project.repos.length === 1 ? 'repo' : 'repos'}`}
            selected={target?.kind === 'project' && target.id === project.id}
            onSelect={() => onPick({ kind: 'project', id: project.id })}
          />
        ))}
        <TargetOption
          label="New project…"
          selected={target?.kind === 'new'}
          onSelect={() => onPick({ kind: 'new', name: '' })}
        />
        {target?.kind === 'new' && (
          <Input
            autoFocus
            value={target.name}
            aria-label="New project name"
            placeholder="Project name"
            onChange={(e) => onPick({ kind: 'new', name: e.target.value })}
            className="h-8 font-mono text-sm"
          />
        )}
        {holder && (
          <TargetOption
            label={`Leave ${holder.name}`}
            hint="no project"
            selected={target?.kind === 'none'}
            onSelect={() => onPick({ kind: 'none', from: holder.id })}
          />
        )}
      </span>
      {empties && target !== null && (
        <span className="mt-2 flex text-xs">
          {target.kind === 'none'
            ? `${holder.name} is left empty — you can delete it next.`
            : `${holder.name} is left empty and goes with the move.`}
        </span>
      )}
    </>
  )
}

function TargetOption({
  label,
  hint,
  selected,
  onSelect,
}: {
  label: string
  hint?: string
  selected: boolean
  onSelect: () => void
}) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      onClick={onSelect}
      className={cn(
        'flex w-full items-center justify-between gap-2 rounded-md border px-2.5 py-1.5 text-left font-mono text-sm transition-colors',
        selected
          ? 'border-primary/60 bg-primary/10 text-foreground'
          : 'border-border text-muted-foreground hover:border-ring/50 hover:text-foreground',
      )}
    >
      <span className="truncate">{label}</span>
      {hint && <span className="shrink-0 text-[0.65rem]">{hint}</span>}
    </button>
  )
}
