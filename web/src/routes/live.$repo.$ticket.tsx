import { createFileRoute } from '@tanstack/react-router'

import { useRepoRouteScope } from '@/components/trau'
import { PANE_VALUES, RunView, type PaneTab } from '@/components/trau/run-view'

interface LiveSearch {
  pane?: PaneTab
}

export const Route = createFileRoute('/live/$repo/$ticket')({
  component: LiveRunPage,
  // pane preselects the terminal or diff view. It is read at runtime through nuqs,
  // typed here so the Loop card can link straight into either one.
  validateSearch: (search: Record<string, unknown>): LiveSearch => {
    const pane = PANE_VALUES.find((value) => value === search.pane)
    return pane ? { pane } : {}
  },
})

function LiveRunPage() {
  const { repo, ticket } = Route.useParams()
  useRepoRouteScope(repo)

  return <RunView repo={repo} ticket={ticket} />
}
