import { useEffect } from 'react'
import { useRouterState } from '@tanstack/react-router'

import { loadRecents, recordRecent, saveRecents, visitRecent } from '@/lib/recents'

export function RecentsTracker() {
  const pathname = useRouterState({ select: (s) => s.location.pathname })

  useEffect(() => {
    const entry = visitRecent(pathname, Date.now())
    if (entry) saveRecents(recordRecent(loadRecents(), entry))
  }, [pathname])

  return null
}
