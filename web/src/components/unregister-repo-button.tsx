import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { FolderMinus } from 'lucide-react'

import { unregisterRepo } from '@/lib/instances'

export function UnregisterRepoButton({ repo }: { repo: string }) {
  const queryClient = useQueryClient()
  const [confirming, setConfirming] = useState(false)

  const unregister = useMutation({
    mutationFn: () => unregisterRepo(repo),
    onSuccess: () => {
      setConfirming(false)
      void queryClient.invalidateQueries({ queryKey: ['instances'] })
    },
  })

  if (!confirming) {
    return (
      <button
        type="button"
        onClick={() => setConfirming(true)}
        className="inline-flex w-fit items-center gap-1.5 text-xs text-muted-foreground transition-colors hover:text-destructive"
      >
        <FolderMinus className="size-3.5" />
        Unregister
      </button>
    )
  }

  return (
    <div className="flex flex-col gap-1.5">
      <p className="text-xs text-muted-foreground">
        Unregister {repo}? It drops back to observe-only — its runs stay
        browsable and nothing on disk is deleted.
      </p>
      <div className="flex items-center gap-2">
        <button
          type="button"
          onClick={() => unregister.mutate()}
          disabled={unregister.isPending}
          className="inline-flex items-center gap-1.5 rounded-md bg-destructive px-2.5 py-1 text-xs text-white transition-opacity hover:opacity-90 disabled:opacity-50"
        >
          <FolderMinus className="size-3.5" />
          {unregister.isPending ? 'Unregistering…' : 'Confirm'}
        </button>
        <button
          type="button"
          onClick={() => setConfirming(false)}
          className="text-xs text-muted-foreground transition-colors hover:text-foreground"
        >
          Cancel
        </button>
      </div>
      {unregister.error && (
        <p className="text-xs text-destructive">
          {String((unregister.error as Error).message)}
        </p>
      )}
    </div>
  )
}
