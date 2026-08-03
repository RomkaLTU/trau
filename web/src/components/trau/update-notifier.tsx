import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { toast } from 'sonner'

import { WhatsNewDialog } from '@/components/trau/whats-new-dialog'
import { updateQueryOptions, versionLabel } from '@/lib/update'
import {
  loadRecapped,
  recordRecapped,
  shouldRecap,
  shouldToast,
  useToastedVersion,
} from '@/lib/update-seen'

// UpdateNotifier is the headless watch on /update: a release the user has not
// been told about yet raises one toast, recorded before it fires so a reload —
// or a second tab — stays quiet about that version. A hub that has come back on
// a newer release than the last load saw opens its notes once instead.
export function UpdateNotifier() {
  const update = useQuery(updateQueryOptions)
  const [toasted, recordToasted] = useToastedVersion()
  const recapped = useRef('')
  const [open, setOpen] = useState(false)
  const status = update.data

  useEffect(() => {
    if (!status || !shouldToast(status, toasted)) return
    recordToasted(status.latest)
    toast(`trau ${versionLabel(status.latest)} is available`, {
      action: { label: "What's new", onClick: () => setOpen(true) },
    })
  }, [status, toasted])

  // The mark advances whether or not the recap is shown: one that could not be
  // given now would only describe an install that is no longer new. Each version
  // is settled once per tab, so a tab still polling the hub this one has left
  // cannot reopen notes the user has already dismissed.
  useEffect(() => {
    if (!status || recapped.current === status.running) return
    recapped.current = status.running
    const recap = shouldRecap(status, loadRecapped())
    recordRecapped(status.running)
    if (recap) setOpen(true)
  }, [status])

  if (!status) return null

  return <WhatsNewDialog status={status} open={open} onOpenChange={setOpen} />
}
