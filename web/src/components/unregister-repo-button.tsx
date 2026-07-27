import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { FolderMinus, Loader2 } from 'lucide-react'

import { ConfirmDialog } from '@/components/trau/confirm-dialog'
import { unregisterRepo } from '@/lib/instances'

export function UnregisterRepoButton({ repo }: { repo: string }) {
  const queryClient = useQueryClient()
  const [confirming, setConfirming] = useState(false)

  const unregister = useMutation({
    mutationFn: () => unregisterRepo(repo),
    onSuccess: () => {
      toast.success(`${repo} is observe-only again`)
      void queryClient.invalidateQueries({ queryKey: ['repos'] })
      void queryClient.invalidateQueries({ queryKey: ['instances'] })
    },
    onError: (err) => toast.error(err.message),
  })

  return (
    <>
      <button
        type="button"
        onClick={() => setConfirming(true)}
        disabled={unregister.isPending}
        className="inline-flex w-fit shrink-0 items-center gap-1.5 text-xs text-muted-foreground transition-colors hover:text-destructive disabled:opacity-50"
      >
        {unregister.isPending ? (
          <Loader2 className="size-3.5 animate-spin" />
        ) : (
          <FolderMinus className="size-3.5" />
        )}
        {unregister.isPending ? 'Unregistering…' : 'Unregister'}
      </button>
      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        windowTitle="unregister repo"
        title={`Unregister ${repo}?`}
        description="It drops back to observe-only — its runs stay browsable and nothing on disk is deleted."
        confirmLabel="Unregister"
        destructive
        onConfirm={() => unregister.mutate()}
      />
    </>
  )
}
