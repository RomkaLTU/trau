import { useMemo } from 'react'

import { cn } from '@/lib/utils'
import type { ConfigKey } from '@/lib/config'
import { isModified, themeRoleLabel } from '@/lib/settings'
import { InlineEditor } from '@/components/trau/settings-editor'

export function ThemeGrid({
  keys,
  repo,
  layers,
  hubRestart,
  editingKey,
  onEdit,
  onCancel,
  onSaved,
}: {
  keys: ConfigKey[]
  repo: string
  layers: string[]
  hubRestart: boolean
  editingKey: string | null
  onEdit: (key: string) => void
  onCancel: () => void
  onSaved: (key: string, target: string) => void
}) {
  const byKey = useMemo(
    () => new Map(keys.map((item) => [item.key, item])),
    [keys],
  )
  const editingCfg =
    editingKey && editingKey.startsWith('THEME_')
      ? byKey.get(editingKey)
      : undefined

  return (
    <div className="flex flex-col gap-3">
      <p className="text-xs leading-relaxed text-muted-foreground">
        Per-role hex overrides applied on top of the active theme preset.
      </p>

      <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-4">
        {keys.map((cfg) => {
          const modified = isModified(cfg)
          const isEditing = editingKey === cfg.key
          const unset = cfg.value === ''
          return (
            <button
              key={cfg.key}
              type="button"
              onClick={() => (isEditing ? onCancel() : onEdit(cfg.key))}
              aria-label={`Edit ${cfg.key}`}
              className={cn(
                'flex items-center gap-2 rounded-md border border-border px-2.5 py-2 text-left transition-colors hover:bg-secondary/60',
                isEditing && 'bg-secondary/60 ring-1 ring-ring/60',
                modified && !isEditing && 'bg-warn/[0.06]',
              )}
            >
              <span
                aria-hidden="true"
                className="size-4 shrink-0 rounded border border-border"
                style={{ backgroundColor: unset ? 'transparent' : cfg.value }}
              />
              <span className="flex min-w-0 flex-col">
                <span className="font-mono text-[0.7rem] lowercase text-muted-foreground">
                  {themeRoleLabel(cfg.key)}
                </span>
                <span
                  className={cn(
                    'truncate font-mono text-xs',
                    unset ? 'text-faint' : 'text-foreground',
                  )}
                >
                  {unset ? 'inherit' : cfg.value}
                </span>
              </span>
              {modified && (
                <span
                  aria-hidden="true"
                  className="ml-auto size-1.5 shrink-0 rounded-full bg-warn"
                  title="modified from default"
                />
              )}
            </button>
          )
        })}
      </div>

      {editingCfg && (
        <div className="flex flex-col gap-2 rounded-md border border-border bg-secondary/20 p-3">
          <span className="font-mono text-xs text-foreground">
            {editingCfg.key}
          </span>
          <InlineEditor
            repo={repo}
            item={editingCfg}
            layers={layers}
            hubRestart={hubRestart}
            onCancel={onCancel}
            onSaved={(target) => onSaved(editingCfg.key, target)}
          />
        </div>
      )}
    </div>
  )
}
