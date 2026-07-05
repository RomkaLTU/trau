import { Link, createFileRoute } from '@tanstack/react-router'
import { ArrowLeft } from 'lucide-react'

import { RunView } from '@/components/trau/run-view'

export const Route = createFileRoute('/live/$repo/$ticket')({
  component: LiveRunPage,
})

function LiveRunPage() {
  const { repo, ticket } = Route.useParams()
  return (
    <div className="flex flex-col gap-4">
      <Link
        to="/runs"
        className="inline-flex w-fit items-center gap-1.5 text-sm text-muted-foreground transition-colors hover:text-foreground"
      >
        <ArrowLeft className="size-4" />
        Runs
      </Link>
      <RunView repo={repo} ticket={ticket} />
    </div>
  )
}
