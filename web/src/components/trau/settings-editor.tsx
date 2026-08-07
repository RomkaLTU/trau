import { useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, ChevronsUpDown, Lock, RotateCcw, TriangleAlert, X } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from '@/components/ui/command'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { SegmentedControl } from '@/components/trau/segmented-control'
import { ConfirmDialog } from '@/components/trau/confirm-dialog'
import { cn } from '@/lib/utils'
import {
  activeTracker,
  canResetLayer,
  comboboxFreeEntry,
  confirmsInternalSwitch,
  editorVariant,
  internalSwitchWarning,
  isHexColor,
  shadowNote,
  valueWarning,
} from '@/lib/settings'
import {
  ConfigWriteError,
  configScopeKey,
  configScopeQueryOptions,
  writeConfigIn,
  type ConfigKey,
  type ConfigScope,
  type ConfigWrite,
} from '@/lib/config'

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

export function ValueWarning({ text }: { text: string }) {
  return (
    <p
      role="status"
      className="inline-flex items-start gap-2 rounded-md border border-warn/50 bg-warn/12 px-2.5 py-2 text-xs leading-relaxed text-warn"
    >
      <TriangleAlert className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
      <span>{text}</span>
    </p>
  )
}

function defaultHint(item: ConfigKey): string {
  return item.default === undefined || item.default === ''
    ? '(unset)'
    : item.default
}

export function initialTarget(item: ConfigKey, layers: string[]): string {
  if (item.layer === 'user') return 'user'
  return layers[0] ?? 'project'
}

// A scope with a single layer to write to has no choice to offer.
export function WriteTarget({
  item,
  layers,
  value,
  onChange,
}: {
  item: ConfigKey
  layers: string[]
  value: string
  onChange: (target: string) => void
}) {
  if (layers.length < 2) return null
  return (
    <span className="inline-flex items-center gap-2 font-mono text-xs text-muted-foreground">
      write to:
      <SegmentedControl
        aria-label={`${item.key} write target`}
        options={layers.map((l) => ({ value: l, label: l }))}
        value={value}
        onChange={onChange}
      />
    </span>
  )
}

export function LayerHint({
  target,
  hubRestart,
}: {
  target: string
  hubRestart: boolean
}) {
  return (
    <p className="font-mono text-[0.7rem] text-faint">
      {target === 'user'
        ? 'user layer applies to every repo on this machine'
        : 'project layer applies only to this repo'}
      {hubRestart && ' · applies on hub restart'}
    </p>
  )
}

// A queue_busy refusal gets the blocking ids a line each; anything else is a
// plain failure line.
export function WriteError({ error }: { error: unknown }) {
  const blocked =
    error instanceof ConfigWriteError && error.reason === 'queue_busy'
      ? (error.blocked ?? [])
      : []

  if (blocked.length === 0) {
    return (
      <p className="font-mono text-xs text-fail" role="alert">
        {String((error as Error).message)}
      </p>
    )
  }

  return (
    <div
      role="alert"
      data-slot="config-write-blocked"
      className="flex flex-col gap-1.5 rounded-md border border-fail/50 bg-fail/12 px-2.5 py-2"
    >
      <p className="font-mono text-xs text-fail">
        The queue still holds tracker work — settle or remove{' '}
        {blocked.length === 1 ? 'it' : 'them'} first.
      </p>
      <ul className="flex flex-col gap-0.5">
        {blocked.map((b) => (
          <li
            key={`${b.repo}:${b.id}`}
            className="flex items-center gap-2 font-mono text-[0.7rem]"
          >
            <span className="text-fail">{b.id}</span>
            <span className="text-muted-foreground">{b.status}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}

function draftIsValid(item: ConfigKey, draft: string): boolean {
  if (item.secret) return draft !== ''
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
  repo: ConfigScope
  item: ConfigKey
  layers: string[]
  hubRestart: boolean
  onCancel: () => void
  onSaved: (target: string, unset: boolean) => void
}) {
  const queryClient = useQueryClient()
  const [draft, setDraft] = useState(item.secret ? '' : item.value)
  const [target, setTarget] = useState(() => initialTarget(item, layers))
  const [confirming, setConfirming] = useState(false)
  const confirmed = useRef(false)
  const syncedIssues =
    useQuery(configScopeQueryOptions(repo)).data?.synced_issues ?? 0

  const mutation = useMutation({
    mutationFn: (body: ConfigWrite) => writeConfigIn(repo, body),
    onSuccess: (_data, body) => {
      queryClient.invalidateQueries({ queryKey: configScopeKey(repo) })
      onSaved(body.layer, Boolean(body.unset))
    },
  })

  const valid = draftIsValid(item, draft)
  const write = () => {
    mutation.mutate({ key: item.key, value: draft, layer: target })
  }
  const save = () => {
    if (!valid) return
    if (confirmsInternalSwitch(item, draft, syncedIssues)) {
      setConfirming(true)
      return
    }
    write()
  }
  const reset = () => {
    mutation.mutate({ key: item.key, value: '', layer: item.layer, unset: true })
  }
  // Radix closes the dialog after the confirm handler runs, so the close alone
  // cannot tell a confirmed switch from an abandoned one — only an abandoned one
  // reverts the select.
  const closeConfirm = (open: boolean) => {
    if (open) return
    setConfirming(false)
    if (confirmed.current) {
      confirmed.current = false
      return
    }
    setDraft(item.value)
  }

  const shadow = shadowNote(item.layer, target)
  const warning = valueWarning(item.key, draft)
  const resettable = canResetLayer(item.layer)

  return (
    <div className="flex flex-col gap-3 rounded-md border border-border bg-secondary/30 p-3">
      <ValueEditor
        item={item}
        value={draft}
        onChange={setDraft}
        onSave={save}
        onCancel={onCancel}
      />

      {warning && <ValueWarning text={warning} />}

      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <WriteTarget
          item={item}
          layers={layers}
          value={target}
          onChange={setTarget}
        />
        <span className="font-mono text-[0.7rem] text-faint">
          default: {defaultHint(item)}
        </span>
        <span className="ml-auto flex items-center gap-2">
          {resettable && (
            <Button
              variant="ghost"
              size="sm"
              className="h-7 font-mono text-xs text-muted-foreground hover:text-warn"
              onClick={reset}
              disabled={mutation.isPending}
              title={`Remove ${item.key} from the ${item.layer} layer`}
            >
              <RotateCcw className="size-3.5" aria-hidden="true" />
              Reset to default
            </Button>
          )}
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

      {shadow && (
        <p
          className="inline-flex items-center gap-1.5 font-mono text-[0.7rem] text-warn"
          role="alert"
        >
          <TriangleAlert className="size-3 shrink-0" aria-hidden="true" />
          {shadow}
        </p>
      )}

      <LayerHint target={target} hubRestart={hubRestart} />

      {mutation.error && <WriteError error={mutation.error} />}

      <ConfirmDialog
        open={confirming}
        onOpenChange={closeConfirm}
        windowTitle="switch tracker"
        title="Switch to the internal tracker?"
        description={internalSwitchWarning(syncedIssues, activeTracker([item]))}
        confirmLabel="Switch"
        onConfirm={() => {
          confirmed.current = true
          write()
        }}
        destructive
      />
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

  switch (editorVariant(item)) {
    case 'bool':
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

    case 'select':
      return (
        <select
          value={value}
          onChange={(e) => onChange(e.target.value)}
          aria-label={`${item.key} value`}
          className="w-full max-w-xs rounded-md border border-border bg-card px-2 py-1.5 font-mono text-xs text-foreground focus-visible:border-ring focus-visible:outline-none dark:bg-input"
        >
          {item.options!.map((o) => (
            <option key={o} value={o}>
              {o}
            </option>
          ))}
        </select>
      )

    case 'color': {
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
            <Input
              autoFocus
              type="text"
              value={value}
              onChange={(e) => onChange(e.target.value)}
              onKeyDown={onKeyDown}
              placeholder={defaultHint(item)}
              aria-label={`${item.key} value`}
              spellCheck={false}
              className="h-auto max-w-[10rem] px-2 py-1.5 font-mono text-xs placeholder:text-faint"
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

    case 'combobox':
      return <ModelCombobox item={item} value={value} onChange={onChange} />

    default:
      return (
        <Input
          autoFocus
          type={item.kind === 'int' ? 'number' : 'text'}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          onKeyDown={onKeyDown}
          placeholder={item.secret ? 'enter new secret value' : defaultHint(item)}
          aria-label={`${item.key} value`}
          className="h-auto max-w-md px-2 py-1.5 font-mono text-xs placeholder:text-faint"
        />
      )
  }
}

function ModelCombobox({
  item,
  value,
  onChange,
}: {
  item: ConfigKey
  value: string
  onChange: (v: string) => void
}) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const suggestions = item.suggestions ?? []
  const custom = comboboxFreeEntry(query, suggestions)

  const commit = (next: string) => {
    onChange(next)
    setQuery('')
    setOpen(false)
  }

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          variant="outline"
          size="sm"
          role="combobox"
          aria-expanded={open}
          aria-label={`${item.key} value`}
          className="h-9 w-full max-w-xs justify-between font-mono text-xs"
        >
          <span className={cn('truncate', value === '' && 'text-faint')}>
            {value === '' ? defaultHint(item) : value}
          </span>
          <ChevronsUpDown className="size-3.5 shrink-0 text-muted-foreground" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-72 p-0">
        <Command>
          <CommandInput
            placeholder="Search or type a model id…"
            value={query}
            onValueChange={setQuery}
          />
          <CommandList>
            <CommandEmpty>No matching model.</CommandEmpty>
            <CommandGroup>
              {suggestions.map((model) => (
                <CommandItem
                  key={model}
                  value={model}
                  onSelect={() => commit(model)}
                >
                  <Check
                    className={cn(
                      'size-4',
                      value === model ? 'opacity-100' : 'opacity-0',
                    )}
                  />
                  <span className="flex-1 truncate font-mono text-xs">
                    {model}
                  </span>
                </CommandItem>
              ))}
            </CommandGroup>
            {custom && (
              <>
                <CommandSeparator />
                <CommandGroup>
                  <CommandItem value={custom} onSelect={() => commit(custom)}>
                    <span className="text-muted-foreground">Use</span>
                    <span className="flex-1 truncate font-mono text-xs">
                      {custom}
                    </span>
                  </CommandItem>
                </CommandGroup>
              </>
            )}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}
