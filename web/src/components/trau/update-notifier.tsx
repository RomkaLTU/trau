import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { toast } from 'sonner'

import { WhatsNewDialog } from '@/components/trau/whats-new-dialog'
import { updateQueryOptions, versionLabel } from '@/lib/update'
import { shouldToast, useToastedVersion } from '@/lib/update-seen'

// UpdateNotifier is the headless watch on /update: a release the user has not
// been told about yet raises one toast, recorded before it fires so a reload —
// or a second tab — stays quiet about that version.
export function UpdateNotifier() {
  const update = useQuery(updateQueryOptions)
  const [toasted, recordToasted] = useToastedVersion()
  const [open, setOpen] = useState(false)
  const status = update.data

  useEffect(() => {
    if (!status || !shouldToast(status, toasted)) return
    recordToasted(status.latest)
    toast(`trau ${versionLabel(status.latest)} is available`, {
      action: { label: "What's new", onClick: () => setOpen(true) },
    })
  }, [status, toasted])

  if (!status) return null

  return <WhatsNewDialog status={status} open={open} onOpenChange={setOpen} />
}
