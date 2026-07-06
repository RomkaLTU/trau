import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { FolderPlus } from 'lucide-react'

import { registerRepo } from '@/lib/instances'

export function RegisterRepoForm() {
  const queryClient = useQueryClient()
  const [path, setPath] = useState('')

  const register = useMutation({
    mutationFn: (p: string) => registerRepo(p),
    onSuccess: () => {
      setPath('')
      void queryClient.invalidateQueries({ queryKey: ['instances'] })
    },
  })

  const submit = () => {
    const trimmed = path.trim()
    if (trimmed === '') return
    register.mutate(trimmed)
  }

  return (
    <div className="flex flex-col gap-3 rounded-lg border bg-card p-4">
      <div className="flex items-center gap-2 text-sm font-medium">
        <FolderPlus className="size-4 text-muted-foreground" />
        Register a repo
      </div>
      <p className="text-xs text-muted-foreground">
        Give the absolute path to a git repository on this machine to make it
        startable from the hub — no config edit or serve restart needed.
      </p>
      <input
        type="text"
        value={path}
        onChange={(e) => setPath(e.target.value)}
        onKeyDown={(e) => e.key === 'Enter' && submit()}
        placeholder="/Users/you/Projects/acme"
        className="h-9 w-full rounded-md border bg-transparent px-3 font-mono text-sm outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
      />
      <button
        type="button"
        onClick={submit}
        disabled={register.isPending || path.trim() === ''}
        className="inline-flex w-fit items-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-sm text-primary-foreground transition-opacity hover:opacity-90 disabled:opacity-50"
      >
        <FolderPlus className="size-4" />
        {register.isPending ? 'Registering…' : 'Register'}
      </button>
      {register.data && (
        <p className="text-xs text-emerald-600 dark:text-emerald-400">
          Registered {register.data.name} — start a run below.
        </p>
      )}
      {register.error && (
        <p className="text-xs text-destructive">
          {String((register.error as Error).message)}
        </p>
      )}
    </div>
  )
}
