import { createFileRoute } from '@tanstack/react-router'

import { useRepoRouteScope } from '@/components/trau'
import { RunView } from '@/components/trau/run-view'

export const Route = createFileRoute('/live/$repo/$ticket')({
  component: LiveRunPage,
})

function LiveRunPage() {
  const { repo, ticket } = Route.useParams()
  useRepoRouteScope(repo)

  return <RunView repo={repo} ticket={ticket} />
}
