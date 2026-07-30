import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Check } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { addProjectRepo, createProject } from '@/lib/projects'
import type { RepoView } from '@/lib/instances'
import { cn } from '@/lib/utils'

const fieldClass =
  'h-auto px-2.5 py-1.5 font-mono text-sm placeholder:text-muted-foreground/60'

// NewProjectDialog names a project and files the picked repos into it in one go.
export function NewProjectDialog({
  repos,
  open,
  onOpenChange,
}: {
  repos: readonly RepoView[]
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const queryClient = useQueryClient()
  const [name, setName] = useState('')
  const [picked, setPicked] = useState<string[]>([])

  const create = useMutation({
    mutationFn: async () => {
      const project = await createProject(name)
      for (const root of picked) {
        await addProjectRepo(project.id, root)
      }
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['projects'] })
      close()
    },
  })

  function close() {
    setName('')
    setPicked([])
    onOpenChange(false)
  }

  function toggle(root: string) {
    setPicked((current) =>
      current.includes(root)
        ? current.filter((r) => r !== root)
        : [...current, root],
    )
  }

  return (
    <AlertDialog
      open={open}
      onOpenChange={(next) => {
        if (!next) close()
      }}
    >
      <AlertDialogContent className="gap-0 overflow-hidden border-border bg-popover p-0 shadow-xl sm:max-w-md">
        <div className="flex items-center gap-3 border-b border-border px-4 py-2.5">
          <div className="flex items-center gap-1.5" aria-hidden="true">
            <span className="size-2.5 rounded-full bg-fail" />
            <span className="size-2.5 rounded-full bg-warn" />
            <span className="size-2.5 rounded-full bg-done" />
          </div>
          <span className="font-mono text-xs text-muted-foreground">
            new project
          </span>
        </div>
        <form
          className="flex min-w-0 flex-col gap-5 p-4"
          onSubmit={(e) => {
            e.preventDefault()
            if (name.trim() === '') return
            create.mutate()
          }}
        >
          <div className="flex flex-col gap-2">
            <AlertDialogTitle className="font-mono text-sm font-normal text-foreground">
              Group repos under a project
            </AlertDialogTitle>
            <AlertDialogDescription className="font-sans text-sm leading-relaxed text-muted-foreground">
              A project is a name over repos you already registered. Nothing moves
              on disk and every repo keeps its queue, runs, and history.
            </AlertDialogDescription>
          </div>

          <div className="flex flex-col gap-1.5">
            <label
              htmlFor="new-project-name"
              className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground"
            >
              name
            </label>
            <Input
              id="new-project-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="e.g. Acme Platform"
              autoFocus
              className={fieldClass}
            />
          </div>

          <div className="flex flex-col gap-1.5">
            <span className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
              repos
            </span>
            {repos.length === 0 ? (
              <p className="font-mono text-xs text-muted-foreground">
                no repos yet — register one first
              </p>
            ) : (
              <div className="max-h-48 overflow-y-auto rounded-md border border-border">
                {repos.map((repo) => (
                  <button
                    key={repo.name}
                    type="button"
                    aria-pressed={picked.includes(repo.root)}
                    onClick={() => toggle(repo.root)}
                    className="flex w-full min-w-0 items-center gap-2.5 px-2.5 py-1.5 text-left transition-colors hover:bg-secondary"
                  >
                    <span
                      aria-hidden="true"
                      className={cn(
                        'flex size-4 shrink-0 items-center justify-center rounded border',
                        picked.includes(repo.root)
                          ? 'border-primary bg-primary text-primary-foreground'
                          : 'border-border',
                      )}
                    >
                      {picked.includes(repo.root) ? (
                        <Check className="size-3" />
                      ) : null}
                    </span>
                    <span className="flex min-w-0 flex-1 flex-col">
                      <span className="truncate font-mono text-sm text-foreground">
                        {repo.name}
                      </span>
                      <span className="truncate font-mono text-[0.65rem] text-muted-foreground">
                        {repo.root}
                      </span>
                    </span>
                  </button>
                ))}
              </div>
            )}
          </div>

          {create.error ? (
            <p
              className="whitespace-pre-line font-mono text-xs text-fail"
              role="alert"
            >
              {(create.error as Error).message}
            </p>
          ) : null}

          <div className="flex justify-end gap-2 border-t border-border pt-4">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="font-mono"
              onClick={close}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              size="sm"
              className="font-mono"
              disabled={name.trim() === '' || create.isPending}
            >
              {create.isPending ? 'Creating…' : 'Create project'}
            </Button>
          </div>
        </form>
      </AlertDialogContent>
    </AlertDialog>
  )
}
