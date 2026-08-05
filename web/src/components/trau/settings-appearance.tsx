import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, TriangleAlert } from 'lucide-react'

import {
  LayerChip,
  LayerHint,
  ValueWarning,
  WriteError,
  WriteTarget,
  initialTarget,
} from '@/components/trau/settings-editor'
import { useResolvedTheme } from '@/components/trau/theme-toggle'
import { cn } from '@/lib/utils'
import { writeConfig, type ConfigKey } from '@/lib/config'
import { themesQueryOptions, type ThemeSummary } from '@/lib/palette'
import { refreshPalettes } from '@/lib/theme'
import { shadowNote } from '@/lib/settings'
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

  const mutation = useMutation({
    mutationFn: (write: ThemeWrite) =>
      writeConfig(repo, { key: item.key, value: write.slug, layer: write.layer }),
    onSuccess: async (_data, write) => {
      await queryClient.invalidateQueries({ queryKey: ['config', repo] })
      await queryClient.invalidateQueries({ queryKey: ['themes', repo] })
      await refreshPalettes(repo)
      setWritten(write)
      onSaved(item.key, write.layer, false)
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
      </div>

      {banner && <ValueWarning text={banner} />}

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
            />
          ))}
        </div>
      )}

      <LayerHint target={target} hubRestart={hubRestart} />

      {mutation.error && <WriteError error={mutation.error} />}
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
}: {
  theme: ThemeSummary
  mode: 'light' | 'dark'
  active: boolean
  pending: boolean
  disabled: boolean
  onPick: () => void
}) {
  const roles = previewRoles(theme, mode)
  const strip = swatches(theme, mode)
  const fallback = modeFallbackNote(theme)
  const author = theme.author === theme.name ? undefined : theme.author

  return (
    <button
      type="button"
      role="radio"
      aria-checked={active}
      onClick={onPick}
      disabled={disabled}
      title={`Use the ${theme.name} theme`}
      className={cn(
        'flex flex-col gap-2.5 rounded-lg border border-border bg-card p-3 text-left transition-opacity',
        pending && 'opacity-60',
        disabled && !pending && 'cursor-default',
      )}
      style={{
        backgroundColor: roles?.ink,
        borderColor: roles?.border,
        color: roles?.text,
        boxShadow: active && roles ? `0 0 0 2px ${roles.brand}` : undefined,
      }}
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

      <span
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
        {fallback && <span className="leading-relaxed">{fallback}</span>}
      </span>
    </button>
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
