import { useEffect, useState, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import { AlertTriangle, Loader2, RefreshCw, Sparkles, XCircle } from 'lucide-react'

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
  generatedAgo,
  shortSha,
  type AtlasCatalogView,
} from '@/lib/atlas'
import { appFlowsFixture, dataModelFixture } from '@/lib/atlas-fixtures'
import { asAppFlows, asDataModel } from '@/lib/atlas-graph'
import { reposQueryOptions } from '@/lib/runs'
import { standardTitle, usePageTitle } from '@/lib/page-title'
import { cn } from '@/lib/utils'

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

function useRegenerate(repo: string, viewId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => generateView(repo, viewId),
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: ['atlas', repo] }),
  })
}

type RegenerateMutation = ReturnType<typeof useRegenerate>

function AtlasViewPanel({
  repo,
  view,
}: {
  repo: string
  view: AtlasCatalogView
}) {
  const regenerate = useRegenerate(repo, view.id)
  const document = useQuery(
    atlasViewQueryOptions(repo, view.id, view.has_document, view.version),
  )

  if (!view.has_document) {
    return <GeneratePanel repo={repo} view={view} regenerate={regenerate} />
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

  const diagram =
    view.flavor === 'data-model' ? (
      <DataModelView doc={asDataModel(document.data.document)} />
    ) : (
      <AppFlowsView doc={asAppFlows(document.data.document)} />
    )

  return (
    <div className="flex flex-1 flex-col gap-3">
      <ViewBanner view={view} regenerate={regenerate} />
      {diagram}
      <ViewFooter view={view} regenerate={regenerate} />
    </div>
  )
}

function ViewBanner({
  view,
  regenerate,
}: {
  view: AtlasCatalogView
  regenerate: RegenerateMutation
}) {
  if (view.generating) {
    return (
      <Banner tone="teal" spin>
        Regenerating {view.title}… reading the repo. The current diagram stays
        until the new one is ready.
      </Banner>
    )
  }
  if (view.error !== '') {
    return (
      <Banner
        tone="fail"
        action={
          <RegenerateButton
            regenerate={regenerate}
            generating={false}
            label="Retry"
          />
        }
      >
        Generation failed: {view.error}
      </Banner>
    )
  }
  if (view.stale > 0) {
    return (
      <Banner
        tone="warn"
        action={
          <RegenerateButton
            regenerate={regenerate}
            generating={false}
            label="Regenerate"
          />
        }
      >
        {view.stale} {view.stale === 1 ? 'merge' : 'merges'} behind — regenerate
        to refresh this View.
      </Banner>
    )
  }
  return null
}

const BANNER_TONE = {
  teal: { box: 'border-teal/40 bg-teal/5', icon: Loader2 },
  warn: { box: 'border-warn/40 bg-warn/5', icon: AlertTriangle },
  fail: { box: 'border-fail/40 bg-fail/5', icon: XCircle },
} as const

function Banner({
  tone,
  spin,
  action,
  children,
}: {
  tone: keyof typeof BANNER_TONE
  spin?: boolean
  action?: ReactNode
  children: ReactNode
}) {
  const { box, icon: Icon } = BANNER_TONE[tone]
  return (
    <div
      role="status"
      className={cn(
        'flex items-center justify-between gap-3 rounded-lg border px-4 py-3',
        box,
      )}
    >
      <div className="flex items-center gap-2.5">
        <Icon
          className={cn('size-4 shrink-0 text-foreground', spin && 'animate-spin')}
          aria-hidden="true"
        />
        <p className="font-mono text-sm leading-relaxed text-foreground">
          {children}
        </p>
      </div>
      {action}
    </div>
  )
}

function ViewFooter({
  view,
  regenerate,
}: {
  view: AtlasCatalogView
  regenerate: RegenerateMutation
}) {
  const meta = [
    `v${view.version}`,
    view.commit !== '' ? shortSha(view.commit) : null,
    view.generated_at !== '' ? `generated ${generatedAgo(view.generated_at)}` : null,
    view.cost_usd !== null ? `$${view.cost_usd.toFixed(2)}` : null,
  ].filter((entry): entry is string => entry !== null)

  return (
    <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 border-t border-border pt-3">
      <span className="font-mono text-xs text-muted-foreground">
        {meta.join(' · ')}
      </span>
      <RegenerateButton
        regenerate={regenerate}
        generating={view.generating}
        label="Regenerate"
      />
    </div>
  )
}

function RegenerateButton({
  regenerate,
  generating,
  label,
}: {
  regenerate: RegenerateMutation
  generating: boolean
  label: string
}) {
  const busy = generating || regenerate.isPending
  return (
    <Button
      variant="outline"
      size="sm"
      onClick={() => regenerate.mutate()}
      disabled={busy}
    >
      <RefreshCw className={cn('size-3.5', busy && 'animate-spin')} />
      {generating ? 'Regenerating…' : regenerate.isPending ? 'Starting…' : label}
    </Button>
  )
}

function GeneratePanel({
  repo,
  view,
  regenerate,
}: {
  repo: string
  view: AtlasCatalogView
  regenerate: RegenerateMutation
}) {
  if (view.generating) {
    return (
      <EmptyState
        className="min-h-[360px]"
        message={`Generating ${view.title}… an agent is reading ${repo} and mapping it. This can take a few minutes.`}
        actions={
          <span className="inline-flex items-center gap-2 font-mono text-xs text-teal">
            <Loader2 className="size-4 animate-spin" aria-hidden="true" />
            Generation in progress…
          </span>
        }
      />
    )
  }

  const failed = view.error !== ''
  return (
    <EmptyState
      className="min-h-[360px]"
      message={
        failed
          ? `Generating ${view.title} for ${repo} failed.`
          : `No ${view.title} generated for ${repo} yet. Generate one — an agent reads the repo and maps it.`
      }
      actions={
        <div className="flex flex-col items-center gap-2">
          <Button
            onClick={() => regenerate.mutate()}
            disabled={regenerate.isPending}
          >
            <Sparkles className="size-4" />
            {regenerate.isPending
              ? 'Starting…'
              : failed
                ? `Retry ${view.title}`
                : `Generate ${view.title}`}
          </Button>
          {failed && (
            <p className="max-w-md text-center font-mono text-xs text-fail">
              {view.error}
            </p>
          )}
          {regenerate.error && (
            <p className="font-mono text-xs text-destructive">
              {String((regenerate.error as Error).message)}
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
