import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, Copy, Palette, Trash2, TriangleAlert } from 'lucide-react'

import {
  LayerChip,
  LayerHint,
  ValueWarning,
  WriteError,
  WriteTarget,
  initialTarget,
} from '@/components/trau/settings-editor'
import { ConfirmDialog } from '@/components/trau/confirm-dialog'
import { ThemeEditor } from '@/components/trau/theme-editor'
import { useResolvedTheme } from '@/components/trau/theme-toggle'
import { cn } from '@/lib/utils'
import { writeConfig, type ConfigKey } from '@/lib/config'
import {
  deleteTheme,
  fetchThemeDocument,
  themesQueryOptions,
  type ThemeSummary,
} from '@/lib/palette'
import { refreshPalettes } from '@/lib/theme'
import { shadowNote } from '@/lib/settings'
import {
  asThemeDocument,
  draftFromDocument,
  type ThemeDraft,
} from '@/lib/theme-editor'
import {
  definedModes,
  modeFallbackNote,
  previewRoles,
  shadowedWriteNote,
  swatches,
} from '@/lib/appearance'

interface ThemeWrite {
  slug: string
  layer: string
}

interface SavedTheme {
  slug: string
  name: string
  replaced: boolean
}

export function ThemePicker({
  repo,
  item,
  layers,
  hubRestart,
  onSaved,
}: {
  repo: string
  item: ConfigKey
  layers: string[]
  hubRestart: boolean
  onSaved: (key: string, target: string, unset: boolean) => void
}) {
  const queryClient = useQueryClient()
  const mode = useResolvedTheme()
  const { data, error, isPending } = useQuery(themesQueryOptions(repo))
  const [target, setTarget] = useState(() => initialTarget(item, layers))
  const [written, setWritten] = useState<ThemeWrite | null>(null)
  const [draft, setDraft] = useState<ThemeDraft | null>(null)
  const [opening, setOpening] = useState(false)
  const [openError, setOpenError] = useState<string | null>(null)
  const [saved, setSaved] = useState<SavedTheme | null>(null)
  const [removing, setRemoving] = useState<ThemeSummary | null>(null)

  const mutation = useMutation({
    mutationFn: (write: ThemeWrite) =>
      writeConfig(repo, { key: item.key, value: write.slug, layer: write.layer }),
    onSuccess: async (_data, write) => {
      await queryClient.invalidateQueries({ queryKey: ['config', repo] })
      await queryClient.invalidateQueries({ queryKey: ['themes'] })
      await refreshPalettes(repo)
      setWritten(write)
      setSaved(null)
      onSaved(item.key, write.layer, false)
    },
  })

  const removal = useMutation({
    mutationFn: (slug: string) => deleteTheme(slug),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['themes'] })
      await queryClient.invalidateQueries({ queryKey: ['config', repo] })
      await refreshPalettes(repo)
      setRemoving(null)
    },
  })

  const themes = data?.themes ?? []
  const active = data?.active ?? item.value
  const shadow = shadowNote(item.layer, target)
  const banner =
    written && shadow
      ? shadowedWriteNote(written.slug, written.layer, item.layer)
      : shadow
  const changeTarget = (next: string) => {
    setWritten(null)
    setTarget(next)
  }

  // The editor always starts from a theme that already exists — the active one
  // for 'Create theme', the picked card for 'Duplicate & edit' — so a duplicated
  // theme keeps the vars and tui blocks its source pinned.
  const openEditor = (slug: string, name: string) => {
    setOpening(true)
    setOpenError(null)
    setSaved(null)
    fetchThemeDocument(slug)
      .then((body) => {
        const doc = asThemeDocument(body)
        if (!doc) throw new Error(`${slug} is not a theme this build can read`)
        setDraft(draftFromDocument(doc, name))
      })
      .catch((err: unknown) => setOpenError((err as Error).message))
      .finally(() => setOpening(false))
  }

  return (
    <div className="flex flex-col gap-3">
      <p className="text-xs leading-relaxed text-muted-foreground">
        Each card previews its theme in the mode you are viewing; the light/dark
        toggle itself stays in the sidebar.
      </p>

      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <WriteTarget
          item={item}
          layers={layers}
          value={target}
          onChange={changeTarget}
        />
        <span className="inline-flex items-center gap-2 font-mono text-[0.7rem] text-faint">
          resolved from
          <LayerChip layer={item.layer} />
        </span>
        <span className="font-mono text-[0.7rem] text-faint">
          default: {item.default === undefined || item.default === '' ? '(unset)' : item.default}
        </span>
        {draft === null && (
          <button
            type="button"
            onClick={() => openEditor(active, 'My Theme')}
            disabled={opening || themes.length === 0}
            className="ml-auto inline-flex items-center gap-1.5 rounded-md border border-border px-2.5 py-1 font-mono text-xs text-muted-foreground transition-colors hover:bg-secondary/60 hover:text-foreground"
          >
            <Palette className="size-3.5" aria-hidden="true" />
            {opening ? 'opening…' : 'Create theme'}
          </button>
        )}
      </div>

      {banner && <ValueWarning text={banner} />}
      {data?.note && <ValueWarning text={data.note} />}
      {openError && <WriteError error={new Error(openError)} />}

      {saved && (
        <div className="flex flex-wrap items-center gap-2 rounded-md border border-done/50 bg-done/10 px-3 py-2 font-mono text-xs text-done">
          <Check className="size-3.5" aria-hidden="true" />
          <span>
            {saved.replaced ? 'Replaced' : 'Saved'} “{saved.name}” as{' '}
            {saved.slug}.json
          </span>
          {saved.slug !== active && (
            <button
              type="button"
              onClick={() => mutation.mutate({ slug: saved.slug, layer: target })}
              disabled={mutation.isPending}
              className="rounded border border-done/60 px-2 py-0.5 transition-colors hover:bg-done/20"
            >
              activate it
            </button>
          )}
          <button
            type="button"
            onClick={() => setSaved(null)}
            className="ml-auto text-done/70 transition-colors hover:text-done"
          >
            dismiss
          </button>
        </div>
      )}

      {draft && (
        <ThemeEditor
          draft={draft}
          themes={themes}
          onCancel={() => setDraft(null)}
          onSaved={(slug, name, replaced) => {
            setDraft(null)
            setSaved({ slug, name, replaced })
          }}
        />
      )}

      {error ? (
        <p
          className="inline-flex items-center gap-2 font-mono text-xs text-fail"
          role="alert"
        >
          <TriangleAlert className="size-3.5" aria-hidden="true" />
          {String((error as Error).message)}
        </p>
      ) : isPending ? (
        <ThemeCardsSkeleton />
      ) : (
        <div
          className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3"
          role="radiogroup"
          aria-label="Theme"
        >
          {themes.map((summary) => (
            <ThemeCard
              key={summary.slug}
              theme={summary}
              mode={mode}
              active={summary.slug === active}
              pending={
                mutation.isPending && mutation.variables?.slug === summary.slug
              }
              disabled={mutation.isPending}
              onPick={() => mutation.mutate({ slug: summary.slug, layer: target })}
              onDuplicate={() => openEditor(summary.slug, `${summary.name} copy`)}
              onDelete={
                summary.origin === 'local' ? () => setRemoving(summary) : undefined
              }
            />
          ))}
        </div>
      )}

      <LayerHint target={target} hubRestart={hubRestart} />

      {mutation.error && <WriteError error={mutation.error} />}
      {removal.error && <WriteError error={removal.error} />}

      <ConfirmDialog
        open={removing !== null}
        onOpenChange={(open) => !open && setRemoving(null)}
        windowTitle="delete theme"
        title={`Delete the saved theme “${removing?.name ?? ''}”?`}
        description={
          removing?.slug === active
            ? `${removing?.slug}.json is removed from the themes directory. It is the active theme, so the UI falls back to the default one.`
            : `${removing?.slug}.json is removed from the themes directory.`
        }
        confirmLabel="Delete"
        destructive
        confirmDisabled={removal.isPending}
        onConfirm={() => removing && removal.mutate(removing.slug)}
      />
    </div>
  )
}

function ThemeCard({
  theme,
  mode,
  active,
  pending,
  disabled,
  onPick,
  onDuplicate,
  onDelete,
}: {
  theme: ThemeSummary
  mode: 'light' | 'dark'
  active: boolean
  pending: boolean
  disabled: boolean
  onPick: () => void
  onDuplicate: () => void
  onDelete?: () => void
}) {
  const roles = previewRoles(theme, mode)
  const strip = swatches(theme, mode)
  const fallback = modeFallbackNote(theme)
  const author = theme.author === theme.name ? undefined : theme.author

  return (
    <div
      className={cn(
        'flex flex-col gap-2.5 rounded-lg border border-border bg-card p-3 transition-opacity',
        pending && 'opacity-60',
      )}
      style={{
        backgroundColor: roles?.ink,
        borderColor: roles?.border,
        color: roles?.text,
        boxShadow: active && roles ? `0 0 0 2px ${roles.brand}` : undefined,
      }}
    >
      <button
        type="button"
        role="radio"
        aria-checked={active}
        onClick={onPick}
        disabled={disabled}
        title={`Use the ${theme.name} theme`}
        className={cn(
          'flex flex-col gap-2.5 text-left',
          disabled && !pending && 'cursor-default',
        )}
      >
        <span className="flex items-start gap-2">
          <span className="flex min-w-0 flex-col">
            <span className="truncate text-sm font-medium">{theme.name}</span>
            {author && (
              <span
                className="truncate font-mono text-[0.65rem]"
                style={{ color: roles?.subtle }}
              >
                {author}
              </span>
            )}
          </span>
          {active && (
            <span
              className="ml-auto inline-flex shrink-0 items-center gap-1 font-mono text-[0.65rem]"
              style={{ color: roles?.brand }}
            >
              <Check className="size-3.5" aria-hidden="true" />
              active
            </span>
          )}
        </span>

        <span className="flex gap-1" aria-hidden="true">
          {strip.map((swatch) => (
            <span
              key={swatch.role}
              title={swatch.role}
              className="h-6 flex-1 rounded-sm"
              style={{ backgroundColor: swatch.color }}
            />
          ))}
        </span>
      </button>

      <div
        className="flex flex-wrap items-center gap-1 font-mono text-[0.65rem]"
        style={{ color: roles?.subtle }}
      >
        {definedModes(theme).map((defined) => (
          <span
            key={defined}
            className="rounded-full border px-1.5 py-0.5 leading-none"
            style={{ borderColor: roles?.border }}
          >
            {defined}
          </span>
        ))}
        {theme.origin === 'local' && (
          <span
            className="rounded-full border px-1.5 py-0.5 leading-none"
            style={{ borderColor: roles?.brand, color: roles?.brand }}
          >
            local
          </span>
        )}
        {fallback && <span className="leading-relaxed">{fallback}</span>}

        <span className="ml-auto flex shrink-0 items-center gap-1">
          <button
            type="button"
            onClick={onDuplicate}
            title={`Duplicate ${theme.name} and edit the copy`}
            aria-label={`Duplicate ${theme.name} and edit the copy`}
            className="rounded p-1 transition-opacity hover:opacity-70"
          >
            <Copy className="size-3.5" aria-hidden="true" />
          </button>
          {onDelete && (
            <button
              type="button"
              onClick={onDelete}
              title={`Delete ${theme.name}`}
              aria-label={`Delete ${theme.name}`}
              className="rounded p-1 transition-opacity hover:opacity-70"
            >
              <Trash2 className="size-3.5" aria-hidden="true" />
            </button>
          )}
        </span>
      </div>
    </div>
  )
}

function ThemeCardsSkeleton() {
  return (
    <div
      className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3"
      aria-busy="true"
      aria-label="Loading themes"
    >
      {[0, 1, 2].map((i) => (
        <div
          key={i}
          className="flex flex-col gap-2.5 rounded-lg border border-border p-3"
        >
          <div className="h-3 w-24 animate-pulse rounded bg-secondary" />
          <div className="h-6 animate-pulse rounded bg-secondary/70" />
          <div className="h-3 w-16 animate-pulse rounded bg-secondary/70" />
        </div>
      ))}
    </div>
  )
}
