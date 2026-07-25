import { memo, useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { DiffModeEnum, DiffView } from '@git-diff-view/react'
import '@git-diff-view/react/styles/diff-view.css'

import { EmptyState } from '@/components/trau/empty-state'
import { SegmentedControl } from '@/components/trau/segmented-control'
import { useResolvedTheme } from '@/components/trau/theme-toggle'
import type { ResolvedTheme } from '@/lib/theme'
import { cn } from '@/lib/utils'
import {
  loadDiffLayout,
  RunDiffError,
  runDiffQueryOptions,
  storeDiffLayout,
  type DiffLayout,
  type RunDiffFile,
} from '@/lib/rundiff'

const LAYOUT_OPTIONS = [
  { value: 'split', label: 'Split' },
  { value: 'inline', label: 'Inline' },
] as const

// LANGS maps an extension to the grammar name the bundled highlighter knows,
// which is highlight.js naming — hence xml for html and no separate yml.
const LANGS: Record<string, string> = {
  css: 'css',
  go: 'go',
  html: 'xml',
  js: 'javascript',
  json: 'json',
  jsx: 'jsx',
  md: 'markdown',
  mjs: 'javascript',
  py: 'python',
  sh: 'bash',
  sql: 'sql',
  svg: 'xml',
  ts: 'typescript',
  tsx: 'tsx',
  xml: 'xml',
  yaml: 'yaml',
  yml: 'yaml',
}

function fileLang(path: string): string {
  const ext = path.slice(path.lastIndexOf('.') + 1).toLowerCase()
  return LANGS[ext] ?? 'plaintext'
}

// useInRange reports whether a node has come within a screen of the viewport, so a
// run touching dozens of files only pays to highlight the ones being looked at.
function useInRange() {
  const ref = useRef<HTMLDivElement>(null)
  const [inRange, setInRange] = useState(false)

  useEffect(() => {
    const node = ref.current
    if (!node || inRange) return
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) setInRange(true)
      },
      { rootMargin: '300px' },
    )
    observer.observe(node)
    return () => observer.disconnect()
  }, [inRange])

  return [ref, inRange] as const
}

const DiffFileView = memo(function DiffFileView({
  file,
  layout,
  theme,
}: {
  file: RunDiffFile
  layout: DiffLayout
  theme: ResolvedTheme
}) {
  const lang = fileLang(file.path)
  const [ref, inRange] = useInRange()

  return (
    <section className="overflow-hidden rounded-lg border border-border bg-card">
      <header className="flex flex-wrap items-center justify-between gap-3 border-b border-border px-4 py-2.5">
        <span className="flex min-w-0 items-center gap-2 font-mono text-xs">
          <span className="truncate text-foreground">{file.path}</span>
          {file.old_path && (
            <span className="shrink-0 text-faint">← {file.old_path}</span>
          )}
          <span className="shrink-0 rounded-md border border-border bg-secondary/50 px-2 py-0.5 text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
            {file.status}
          </span>
        </span>
        <span className="shrink-0 font-mono text-xs tabular-nums">
          <span className="text-done">+{file.additions}</span>{' '}
          <span className="text-fail">−{file.deletions}</span>
        </span>
      </header>
      {file.patch ? (
        <div
          ref={ref}
          className={cn('overflow-x-auto text-xs', !inRange && 'min-h-20')}
        >
          {inRange && (
            <DiffView
              data={{
                hunks: [file.patch],
                oldFile: { fileName: file.old_path ?? file.path, fileLang: lang },
                newFile: { fileName: file.path, fileLang: lang },
              }}
              diffViewMode={
                layout === 'split' ? DiffModeEnum.Split : DiffModeEnum.Unified
              }
              diffViewTheme={theme}
              diffViewFontSize={12}
              diffViewHighlight
              diffViewWrap
            />
          )}
        </div>
      ) : (
        <p className="px-4 py-3 font-mono text-xs text-muted-foreground">
          {file.binary
            ? 'Binary file — no preview'
            : 'Patch omitted — this file is too large to render'}
        </p>
      )}
    </section>
  )
})

export function RunDiff({ repo, ticket }: { repo: string; ticket: string }) {
  const [layout, setLayoutState] = useState<DiffLayout>(loadDiffLayout)
  const theme = useResolvedTheme()
  const { data, error, isPending } = useQuery(runDiffQueryOptions(repo, ticket))

  const setLayout = (next: DiffLayout) => {
    storeDiffLayout(next)
    setLayoutState(next)
  }

  const notFound = error instanceof RunDiffError && error.status === 404

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <span className="font-mono text-xs text-muted-foreground">
          {data ? `${data.branch} ↔ ${data.base}` : ''}
        </span>
        <SegmentedControl
          aria-label="Diff layout"
          options={LAYOUT_OPTIONS}
          value={layout}
          onChange={setLayout}
        />
      </div>

      {notFound ? (
        <EmptyState message="No diff for this run yet — it has no branch to compare against." />
      ) : error ? (
        <p className="font-mono text-sm text-destructive">{error.message}</p>
      ) : isPending ? (
        <p className="font-mono text-xs text-muted-foreground">
          Reading the working tree…
        </p>
      ) : data.files.length === 0 ? (
        <EmptyState message="No changes yet" />
      ) : (
        <>
          {data.truncated && (
            <p className="font-mono text-xs text-warn">
              ⚠ This diff is too large to send in full — the largest files show
              their counts without a patch.
            </p>
          )}
          {data.files.map((file) => (
            <DiffFileView
              key={file.path}
              file={file}
              layout={layout}
              theme={theme}
            />
          ))}
        </>
      )}
    </div>
  )
}
