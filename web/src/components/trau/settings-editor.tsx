import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Check, Lock, X } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { SegmentedControl } from '@/components/trau/segmented-control'
import { cn } from '@/lib/utils'
import { isHexColor } from '@/lib/settings'
import { writeConfig, type ConfigKey, type ConfigWrite } from '@/lib/config'

const LAYER_STYLES: Record<string, string> = {
  project: 'border-teal/50 bg-teal/12 text-teal',
  user: 'border-info/50 bg-info/12 text-info',
  default: 'border-faint/50 bg-faint/12 text-faint',
  'env var': 'border-warn/50 bg-warn/12 text-warn',
  local: 'border-done/50 bg-done/12 text-done',
  CLI: 'border-cli/50 bg-cli/12 text-cli',
}

export function LayerChip({ layer }: { layer: string }) {
  return (
    <span
      className={cn(
        'inline-flex shrink-0 items-center rounded border px-1.5 py-0.5 font-mono text-[0.65rem] leading-none',
        LAYER_STYLES[layer] ?? LAYER_STYLES.default,
      )}
    >
      {layer}
    </span>
  )
}

export function SecretChip() {
  return (
    <span className="inline-flex shrink-0 items-center gap-1 rounded border border-warn/50 bg-warn/12 px-1.5 py-0.5 font-mono text-[0.65rem] leading-none text-warn">
      <Lock className="size-2.5" aria-hidden="true" />
      secret
    </span>
  )
}

function defaultHint(item: ConfigKey): string {
  return item.default === undefined || item.default === ''
    ? '(unset)'
    : item.default
}

function initialTarget(item: ConfigKey, layers: string[]): string {
  if (item.layer === 'user') return 'user'
  return layers[0] ?? 'project'
}

function draftIsValid(item: ConfigKey, draft: string): boolean {
  if (item.kind === 'color') return draft === '' || isHexColor(draft)
  return true
}

export function InlineEditor({
  repo,
  item,
  layers,
  hubRestart,
  onCancel,
  onSaved,
}: {
  repo: string
  item: ConfigKey
  layers: string[]
  hubRestart: boolean
  onCancel: () => void
  onSaved: (target: string) => void
}) {
  const queryClient = useQueryClient()
  const [draft, setDraft] = useState(item.secret ? '' : item.value)
  const [target, setTarget] = useState(() => initialTarget(item, layers))

  const mutation = useMutation({
    mutationFn: (body: ConfigWrite) => writeConfig(repo, body),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['config', repo] })
      onSaved(target)
    },
  })

  const valid = draftIsValid(item, draft)
  const save = () => {
    if (!valid) return
    mutation.mutate({ key: item.key, value: draft, layer: target })
  }

  return (
    <div className="flex flex-col gap-3 rounded-md border border-border bg-secondary/30 p-3">
      <ValueEditor
        item={item}
        value={draft}
        onChange={setDraft}
        onSave={save}
        onCancel={onCancel}
      />

      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <span className="inline-flex items-center gap-2 font-mono text-xs text-muted-foreground">
          write to:
          <SegmentedControl
            aria-label={`${item.key} write target`}
            options={layers.map((l) => ({ value: l, label: l }))}
            value={target}
            onChange={setTarget}
          />
        </span>
        <span className="font-mono text-[0.7rem] text-faint">
          default: {defaultHint(item)}
        </span>
        <span className="ml-auto flex items-center gap-2">
          <Button
            variant="ghost"
            size="sm"
            className="h-7 font-mono text-xs"
            onClick={onCancel}
            disabled={mutation.isPending}
          >
            <X className="size-3.5" aria-hidden="true" />
            Cancel
          </Button>
          <Button
            size="sm"
            className="h-7 font-mono text-xs"
            onClick={save}
            disabled={mutation.isPending || !valid}
          >
            <Check className="size-3.5" aria-hidden="true" />
            {mutation.isPending ? 'Saving…' : 'Save'}
          </Button>
        </span>
      </div>

      <p className="font-mono text-[0.7rem] text-faint">
        {target === 'user'
          ? 'user layer applies to every repo on this machine'
          : 'project layer applies only to this repo'}
        {hubRestart && ' · applies on hub restart'}
      </p>

      {mutation.error && (
        <p className="font-mono text-xs text-fail">
          {String((mutation.error as Error).message)}
        </p>
      )}
    </div>
  )
}

function ValueEditor({
  item,
  value,
  onChange,
  onSave,
  onCancel,
}: {
  item: ConfigKey
  value: string
  onChange: (v: string) => void
  onSave: () => void
  onCancel: () => void
}) {
  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.nativeEvent.isComposing) return
    if (e.key === 'Enter') onSave()
    if (e.key === 'Escape') onCancel()
  }

  if (item.bool) {
    return (
      <SegmentedControl
        aria-label={`${item.key} value`}
        options={[
          { value: '1', label: 'on' },
          { value: '0', label: 'off' },
        ]}
        value={value === '1' ? '1' : '0'}
        onChange={onChange}
      />
    )
  }

  if (item.options && item.options.length > 0) {
    return (
      <select
        value={value}
        onChange={(e) => onChange(e.target.value)}
        aria-label={`${item.key} value`}
        className="w-full max-w-xs rounded-md border border-border bg-input px-2 py-1.5 font-mono text-xs text-foreground focus-visible:border-ring focus-visible:outline-none"
      >
        {item.options.map((o) => (
          <option key={o} value={o}>
            {o}
          </option>
        ))}
      </select>
    )
  }

  if (item.kind === 'color') {
    const valid = value === '' || isHexColor(value)
    return (
      <div className="flex flex-col gap-1.5">
        <div className="flex items-center gap-2">
          <input
            type="color"
            value={isHexColor(value) ? value : '#000000'}
            onChange={(e) => onChange(e.target.value)}
            aria-label={`${item.key} color`}
            className="size-8 shrink-0 cursor-pointer rounded border border-border bg-input p-0.5"
          />
          <input
            autoFocus
            type="text"
            value={value}
            onChange={(e) => onChange(e.target.value)}
            onKeyDown={onKeyDown}
            placeholder={defaultHint(item)}
            aria-label={`${item.key} value`}
            spellCheck={false}
            className="w-full max-w-[10rem] rounded-md border border-border bg-input px-2 py-1.5 font-mono text-xs text-foreground placeholder:text-faint focus-visible:border-ring focus-visible:outline-none"
          />
        </div>
        {!valid && (
          <span className="font-mono text-[0.7rem] text-fail">
            expected a hex color like #7d56f4
          </span>
        )}
      </div>
    )
  }

  return (
    <input
      autoFocus
      type={item.kind === 'int' ? 'number' : 'text'}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      onKeyDown={onKeyDown}
      placeholder={item.secret ? 'enter new secret value' : defaultHint(item)}
      aria-label={`${item.key} value`}
      className="w-full max-w-md rounded-md border border-border bg-input px-2 py-1.5 font-mono text-xs text-foreground placeholder:text-faint focus-visible:border-ring focus-visible:outline-none"
    />
  )
}
