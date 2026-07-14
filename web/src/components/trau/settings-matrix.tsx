import { useMemo, useState } from 'react'

import { cn } from '@/lib/utils'
import type { ConfigKey } from '@/lib/config'
import {
  COLUMN_LABELS,
  derivePhaseMatrix,
  isModified,
  routingCellKey,
} from '@/lib/settings'
import { InlineEditor, LayerChip } from '@/components/trau/settings-editor'

export function PhaseMatrix({
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
  onSaved: (key: string, target: string, unset: boolean) => void
}) {
  const model = useMemo(() => derivePhaseMatrix(keys), [keys])
  const byKey = useMemo(
    () => new Map(keys.map((item) => [item.key, item])),
    [keys],
  )
  const [provider, setProvider] = useState(() => model.providers[0] ?? '')

  const activeProvider = model.providers.includes(provider)
    ? provider
    : (model.providers[0] ?? '')
  const phases = model.phases[activeProvider] ?? []
  const columns = model.columns[activeProvider] ?? []

  const editingCfg = editingKey ? byKey.get(editingKey) : undefined
  const editingBelongsHere = editingCfg?.key.startsWith(`${activeProvider}_`)

  return (
    <div className="flex flex-col gap-3">
      <div
        role="tablist"
        aria-label="Provider"
        className="inline-flex w-fit items-center rounded-md border border-border bg-input p-0.5"
      >
        {model.providers.map((p) => (
          <button
            key={p}
            type="button"
            role="tab"
            aria-selected={activeProvider === p}
            onClick={() => {
              setProvider(p)
              onCancel()
            }}
            className={cn(
              'rounded px-3 py-1 font-mono text-xs lowercase transition-colors',
              activeProvider === p
                ? 'bg-secondary text-foreground'
                : 'text-muted-foreground hover:text-foreground',
            )}
          >
            {p.toLowerCase()}
          </button>
        ))}
      </div>

      <div className="overflow-x-auto">
        <table className="w-full border-collapse font-mono text-xs">
          <thead>
            <tr className="border-b border-border text-left text-muted-foreground">
              <th className="px-3 py-2 font-normal uppercase tracking-wider">
                phase
              </th>
              {columns.map((col) => (
                <th
                  key={col}
                  className="px-3 py-2 font-normal uppercase tracking-wider"
                >
                  {COLUMN_LABELS[col]}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {phases.map((phase) => (
              <tr
                key={phase}
                className="border-b border-border/60 last:border-0"
              >
                <td className="px-3 py-2 lowercase text-muted-foreground">
                  {phase.toLowerCase()}
                </td>
                {columns.map((col) => {
                  const key = routingCellKey(activeProvider, phase, col)
                  const cfg = byKey.get(key)
                  if (!cfg) return <td key={col} className="px-3 py-2" />
                  const modified = isModified(cfg)
                  const isEditing = editingKey === key
                  return (
                    <td key={col} className="px-1 py-1">
                      <button
                        type="button"
                        onClick={() => (isEditing ? onCancel() : onEdit(key))}
                        aria-label={`Edit ${key}`}
                        className={cn(
                          'flex w-full items-center gap-1.5 rounded px-2 py-1 text-left transition-colors hover:bg-secondary/60',
                          isEditing && 'bg-secondary/60 ring-1 ring-ring/60',
                          modified && !isEditing && 'bg-warn/[0.06]',
                        )}
                      >
                        {cfg.value === '' ? (
                          <span className="text-faint">inherit</span>
                        ) : (
                          <>
                            <span className="truncate text-foreground">
                              {cfg.value}
                            </span>
                            <LayerChip layer={cfg.layer} />
                          </>
                        )}
                      </button>
                    </td>
                  )
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {editingCfg && editingBelongsHere && (
        <div className="flex flex-col gap-2 rounded-md border border-border bg-secondary/20 p-3">
          <span className="font-mono text-xs text-foreground">
            {editingCfg.key}
          </span>
          {editingCfg.description && (
            <p className="text-xs leading-relaxed text-muted-foreground">
              {editingCfg.description}
            </p>
          )}
          <InlineEditor
            repo={repo}
            item={editingCfg}
            layers={layers}
            hubRestart={hubRestart}
            onCancel={onCancel}
            onSaved={(target, unset) => onSaved(editingCfg.key, target, unset)}
          />
        </div>
      )}
    </div>
  )
}
