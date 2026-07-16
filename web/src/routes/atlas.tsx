import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import { Sparkles } from 'lucide-react'

import {
  EmptyState,
  Eyebrow,
  SegmentedControl,
  useActiveRepo,
} from '@/components/trau'
import { Button } from '@/components/ui/button'
import { AppFlowsView } from '@/components/atlas/app-flows-view'
import { DataModelView } from '@/components/atlas/data-model-view'
import {
  atlasCatalogQueryOptions,
  atlasViewQueryOptions,
  generateView,
  type AtlasCatalogView,
} from '@/lib/atlas'
import { appFlowsFixture, dataModelFixture } from '@/lib/atlas-fixtures'
import { asAppFlows, asDataModel } from '@/lib/atlas-graph'
import { reposQueryOptions } from '@/lib/runs'
import { standardTitle, usePageTitle } from '@/lib/page-title'

type AtlasSearch = { demo?: 'data-model' | 'app-flows' }

export const Route = createFileRoute('/atlas')({
  component: AtlasPage,
  validateSearch: (search: Record<string, unknown>): AtlasSearch => ({
    demo:
      search.demo === 'data-model' || search.demo === 'app-flows'
        ? search.demo
        : undefined,
  }),
  loader: ({ context }) =>
    context.queryClient.ensureQueryData(reposQueryOptions),
})

function AtlasPage() {
  usePageTitle(standardTitle('Atlas'))
  const { repo } = useActiveRepo()
  const { demo } = Route.useSearch()

  return (
    <div className="flex flex-1 flex-col gap-6">
      <header className="flex flex-col gap-2">
        <Eyebrow glyph="active" className="text-teal">
          ATLAS
        </Eyebrow>
        <h1 className="text-balance text-2xl font-semibold tracking-tight text-foreground">
          Atlas
        </h1>
        <p className="text-pretty text-sm leading-relaxed text-muted-foreground">
          Agent-generated architecture Views of the active repo — the data model
          and the app's runtime flows, laid out interactively.
        </p>
      </header>

      {demo ? (
        <DemoAtlas flavor={demo} />
      ) : repo ? (
        <AtlasBoard repo={repo} />
      ) : (
        <EmptyState
          className="min-h-[300px]"
          message="Select a project to view its Atlas."
        />
      )}
    </div>
  )
}

function AtlasBoard({ repo }: { repo: string }) {
  const catalog = useQuery(atlasCatalogQueryOptions(repo))
  const views = catalog.data?.views ?? []
  const [viewId, setViewId] = useState('')
  const active = views.find((v) => v.id === viewId) ?? views[0]

  useEffect(() => {
    if (views.length > 0 && !views.some((v) => v.id === viewId)) {
      setViewId(views[0].id)
    }
  }, [views, viewId])

  if (catalog.isPending) {
    return <p className="font-mono text-sm text-muted-foreground">Loading…</p>
  }
  if (catalog.error) {
    return (
      <p className="font-mono text-sm text-destructive">
        {String(catalog.error)}
      </p>
    )
  }
  if (!active) {
    return (
      <EmptyState
        className="min-h-[300px]"
        message="No Atlas Views available."
      />
    )
  }

  return (
    <div className="flex flex-1 flex-col gap-4">
      <SegmentedControl
        aria-label="Atlas Views"
        options={views.map((v) => ({ value: v.id, label: v.title }))}
        value={active.id}
        onChange={setViewId}
      />
      <AtlasViewPanel key={active.id} repo={repo} view={active} />
    </div>
  )
}

function AtlasViewPanel({
  repo,
  view,
}: {
  repo: string
  view: AtlasCatalogView
}) {
  const document = useQuery(
    atlasViewQueryOptions(repo, view.id, view.has_document),
  )

  if (!view.has_document) {
    return <GeneratePanel repo={repo} view={view} />
  }
  if (document.isPending) {
    return <p className="font-mono text-sm text-muted-foreground">Loading…</p>
  }
  if (document.error || !document.data) {
    return (
      <p className="font-mono text-sm text-destructive">
        {document.error ? String(document.error) : 'Document unavailable.'}
      </p>
    )
  }

  if (view.flavor === 'data-model') {
    return <DataModelView doc={asDataModel(document.data.document)} />
  }
  return <AppFlowsView doc={asAppFlows(document.data.document)} />
}

function GeneratePanel({
  repo,
  view,
}: {
  repo: string
  view: AtlasCatalogView
}) {
  const queryClient = useQueryClient()
  const generate = useMutation({
    mutationFn: () => generateView(repo, view.id),
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: ['atlas', repo] }),
  })

  return (
    <EmptyState
      className="min-h-[360px]"
      message={`No ${view.title} generated for ${repo} yet. Generate one — an agent reads the repo and maps it.`}
      actions={
        <div className="flex flex-col items-center gap-2">
          <Button
            onClick={() => generate.mutate()}
            disabled={generate.isPending}
          >
            <Sparkles className="size-4" />
            {generate.isPending ? 'Starting…' : `Generate ${view.title}`}
          </Button>
          {generate.isSuccess && (
            <p className="font-mono text-xs text-teal">
              Generation started — this can take a few minutes.
            </p>
          )}
          {generate.error && (
            <p className="font-mono text-xs text-destructive">
              {String((generate.error as Error).message)}
            </p>
          )}
        </div>
      }
    />
  )
}

function DemoAtlas({ flavor }: { flavor: 'data-model' | 'app-flows' }) {
  return (
    <div className="flex flex-1 flex-col gap-3">
      <p className="font-mono text-xs text-warn">
        Demo data — sample fixture, not this repo.
      </p>
      {flavor === 'data-model' ? (
        <DataModelView doc={dataModelFixture} />
      ) : (
        <AppFlowsView doc={appFlowsFixture} />
      )}
    </div>
  )
}
