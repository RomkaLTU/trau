import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Pencil } from 'lucide-react'

import { renameProject, type ProjectView } from '@/lib/projects'
import { cn } from '@/lib/utils'

const NAME_CLASS = 'truncate font-mono text-xs tracking-[0.14em]'

// The rename lands on the hub's project row, so the sidebar, the picker and every
// group here follow it without a restart.
export function ProjectNameField({ project }: { project: ProjectView }) {
  const queryClient = useQueryClient()
  const [editing, setEditing] = useState(false)
  const [name, setName] = useState(project.name)

  const rename = useMutation({
    mutationFn: (next: string) => renameProject(project.id, next),
    onSuccess: (renamed) => {
      toast.success(`Project renamed to ${renamed.name}`)
      void queryClient.invalidateQueries({ queryKey: ['projects'] })
    },
    onError: (err: Error) => {
      toast.error(err.message)
      setName(project.name)
    },
  })

  function commit() {
    setEditing(false)
    const next = name.trim()
    if (next === '' || next === project.name) {
      setName(project.name)
      return
    }
    rename.mutate(next)
  }

  if (!editing) {
    return (
      <button
        type="button"
        title="Rename project"
        disabled={rename.isPending}
        onClick={() => {
          setName(project.name)
          setEditing(true)
        }}
        className="group inline-flex min-w-0 items-center gap-1.5 text-muted-foreground transition-colors hover:text-foreground disabled:opacity-50"
      >
        <span className={cn(NAME_CLASS, 'uppercase')}>
          {rename.isPending ? name : project.name}
        </span>
        <Pencil
          className="size-3 shrink-0 opacity-0 transition-opacity group-hover:opacity-100 group-focus-visible:opacity-100"
          aria-hidden="true"
        />
      </button>
    )
  }

  return (
    <input
      autoFocus
      value={name}
      aria-label="Project name"
      onFocus={(e) => e.currentTarget.select()}
      onChange={(e) => setName(e.target.value)}
      onBlur={commit}
      onKeyDown={(e) => {
        if (e.key === 'Enter') commit()
        if (e.key === 'Escape') {
          setName(project.name)
          setEditing(false)
        }
      }}
      className={cn(
        NAME_CLASS,
        'w-48 max-w-full rounded border border-border bg-input px-1.5 py-0.5 text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring/50',
      )}
    />
  )
}
