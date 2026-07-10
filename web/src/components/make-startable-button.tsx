import { useMutation, useQueryClient } from '@tanstack/react-query'
import { FolderPlus } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { registerRepo } from '@/lib/instances'

// MakeStartableButton registers an already-known repo by its root so the hub may
// start loops in it — one click, no path to retype. Invalidates both ['repos']
// (drives the switcher/notices) and ['instances'] so the UI reflects the new
// allowlist immediately rather than on the next poll.
export function MakeStartableButton({
  root,
  name,
  size = 'sm',
  variant = 'default',
  className,
}: {
  root: string
  name?: string
  size?: 'sm' | 'default'
  variant?: 'default' | 'outline'
  className?: string
}) {
  const queryClient = useQueryClient()
  const register = useMutation({
    mutationFn: () => registerRepo(root),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['repos'] })
      void queryClient.invalidateQueries({ queryKey: ['instances'] })
    },
  })

  return (
    <div className="flex flex-col items-start gap-1">
      <Button
        type="button"
        size={size}
        variant={variant}
        className={className}
        disabled={register.isPending}
        onClick={() => register.mutate()}
      >
        <FolderPlus className="size-4" />
        {register.isPending
          ? 'Enabling…'
          : name
            ? `Make ${name} startable`
            : 'Make startable'}
      </Button>
      {register.error && (
        <p className="text-xs text-destructive">
          {(register.error as Error).message}
        </p>
      )}
    </div>
  )
}
