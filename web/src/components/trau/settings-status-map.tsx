import {
  useMemo,
  useState,
  type Dispatch,
  type ReactNode,
  type SetStateAction,
} from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, TriangleAlert } from 'lucide-react'

import { Button } from '@/components/ui/button'
import {
  LayerChip,
  LayerHint,
  WriteError,
  WriteTarget,
  initialTarget,
} from '@/components/trau/settings-editor'
import {
  GroupingFields,
  MappingBlockHeader,
  PinFields,
} from '@/components/trau/status-map-editor'
import { writeConfig, type ConfigKey } from '@/lib/config'
import {
  PIN_KEYS,
  deriveGroupingRows,
  groupingEdited,
  mappingSpec,
  serializeGrouping,
  statusOptionsQueryOptions,
  type GroupingRow,
  type MappingSpec,
  type PinKey,
  type StatusColumn,
  type StatusOptions,
  type StatusPinOption,
} from '@/lib/statusmap'

const SYNC_NOTE =
  'Saved. The board regroups on the next sync pull for this repo.'

const PIN_NOTE = 'Saved. Pins apply to the next write this repo makes.'

// The board reads as no columns at all when it could not be read; a shared
// constant keeps that a stable value rather than a fresh array each render.
const NO_COLUMNS: StatusColumn[] = []

// WriteContext is everything a block needs to commit an edit: the repo, the
// layers it may write to, and the one layer selection both blocks share.
interface WriteContext {
  repo: string
  layers: string[]
  hubRestart: boolean
  target: string
  onTarget: (target: string) => void
  onSaved: (key: string, target: string, unset: boolean) => void
}

// useBlockWrite holds a block's uncommitted edits and the write that commits
// them. `signature` states what `stored` was derived from: the draft restarts
// only when that value changes — a save, or anything else writing the keys — so
// a re-render for any other reason (the sibling block saving, the shared layer
// selector moving) leaves the edits standing.
function useBlockWrite<T>({
  ctx,
  stored,
  signature,
  write,
  savedNote,
  onRestart,
}: {
  ctx: WriteContext
  stored: T
  signature: string
  write: (draft: T) => Promise<string[]>
  savedNote: string
  onRestart?: () => void
}) {
  const queryClient = useQueryClient()
  const [baseline, setBaseline] = useState(signature)
  const [draft, setDraft] = useState(stored)
  const [note, setNote] = useState<string | null>(null)

  if (baseline !== signature) {
    setBaseline(signature)
    setDraft(stored)
    onRestart?.()
  }

  const mutation = useMutation({
    mutationFn: write,
    onSuccess: (written) => {
      queryClient.invalidateQueries({ queryKey: ['config', ctx.repo] })
      if (written.length > 0) ctx.onSaved(written.join(', '), ctx.target, false)
      setNote(savedNote)
    },
  })

  const edit: Dispatch<SetStateAction<T>> = (next) => {
    setNote(null)
    setDraft(next)
  }

  return {
    draft,
    edit,
    note,
    save: () => mutation.mutate(draft),
    saving: mutation.isPending,
    error: mutation.error,
  }
}

export function TrackerAdvanced({
  repo,
  keys,
  layers,
  hubRestart,
  onSaved,
  renderRow,
}: {
  repo: string
  keys: ConfigKey[]
  layers: string[]
  hubRestart: boolean
  onSaved: (key: string, target: string, unset: boolean) => void
  renderRow: (item: ConfigKey) => ReactNode
}) {
  const { data } = useQuery(statusOptionsQueryOptions(repo))
  const spec = data ? mappingSpec(data.provider) : null
  const boardItem = keys.find((item) => item.key === spec?.key)

  if (!data || !spec || !boardItem?.editable) {
    return <>{keys.map(renderRow)}</>
  }
  return (
    <>
      <div className="border-b border-border/60 p-4">
        <StatusMapEditor
          repo={repo}
          keys={keys}
          spec={spec}
          options={data}
          layers={layers}
          hubRestart={hubRestart}
          onSaved={onSaved}
        />
      </div>
      {keys
        .filter((item) => !ownedKeys(spec).includes(item.key))
        .map(renderRow)}
    </>
  )
}

// ownedKeys are the rows the editor took over for this provider. The other
// provider's mapping key is not one of them: it stays an ordinary generic row,
// like any key this repo's tracker does not use.
function ownedKeys(spec: MappingSpec): string[] {
  return [spec.key, ...PIN_KEYS]
}

function StatusMapEditor({
  repo,
  keys,
  spec,
  options,
  layers,
  hubRestart,
  onSaved,
}: {
  repo: string
  keys: ConfigKey[]
  spec: MappingSpec
  options: StatusOptions
  layers: string[]
  hubRestart: boolean
  onSaved: (key: string, target: string, unset: boolean) => void
}) {
  const boardItem = useMemo(
    () => keys.find((item) => item.key === spec.key)!,
    [keys, spec.key],
  )
  const pinItems = useMemo(
    () =>
      PIN_KEYS.map((key) => keys.find((item) => item.key === key)).filter(
        (item): item is ConfigKey => item !== undefined && item.editable,
      ),
    [keys],
  )
  const [target, setTarget] = useState(() => initialTarget(boardItem, layers))
  const ctx: WriteContext = {
    repo,
    layers,
    hubRestart,
    target,
    onTarget: setTarget,
    onSaved,
  }

  return (
    <div className="flex flex-col gap-6">
      {options.error && <FetchFailure options={options} />}
      <GroupingBlock
        ctx={ctx}
        spec={spec}
        item={boardItem}
        columns={options.error ? NO_COLUMNS : options.grouping}
        boardRead={!options.error}
      />
      <PinsBlock
        ctx={ctx}
        spec={spec}
        items={pinItems}
        pinOptions={options.pinOptions}
      />
    </div>
  )
}

function FetchFailure({ options }: { options: StatusOptions }) {
  return (
    <div
      role="alert"
      className="flex flex-col gap-1.5 rounded-md border border-fail/50 bg-fail/10 px-3 py-2.5"
    >
      <p className="inline-flex items-start gap-2 font-mono text-xs leading-relaxed text-fail">
        <TriangleAlert className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
        <span>{options.error}</span>
      </p>
      {options.hint && (
        <p className="pl-[1.375rem] text-xs leading-relaxed text-muted-foreground">
          {options.hint}
        </p>
      )}
      <p className="pl-[1.375rem] text-xs leading-relaxed text-muted-foreground">
        The board could not be read, so only the mapping this repo already
        carries is listed. Editing and saving still work.
      </p>
    </div>
  )
}

function GroupingBlock({
  ctx,
  spec,
  item,
  columns,
  boardRead,
}: {
  ctx: WriteContext
  spec: MappingSpec
  item: ConfigKey
  columns: StatusColumn[]
  boardRead: boolean
}) {
  const stored = useMemo(
    () => deriveGroupingRows(columns, item.value, spec),
    [columns, item.value, spec],
  )

  const {
    draft: rows,
    edit,
    note,
    save,
    saving,
    error,
  } = useBlockWrite({
    ctx,
    stored,
    signature: JSON.stringify([spec.key, columns, item.value]),
    write: async (draft: GroupingRow[]) => {
      await writeConfig(ctx.repo, {
        key: spec.key,
        value: serializeGrouping(draft, spec),
        layer: ctx.target,
      })
      return [spec.key]
    },
    savedNote: SYNC_NOTE,
  })

  const serialized = serializeGrouping(rows, spec)

  return (
    <section className="flex flex-col gap-3">
      <MappingBlockHeader
        title={spec.title}
        keyName={spec.key}
        badge={<LayerChip layer={item.layer} />}
        description={spec.description}
      />

      <GroupingFields
        spec={spec}
        rows={rows}
        onRows={edit}
        boardRead={boardRead}
      />

      <BlockFooter
        ctx={ctx}
        item={item}
        onSave={save}
        saving={saving}
        note={note}
        error={error}
        preview={groupingPreview(
          item.value,
          serialized,
          groupingEdited(rows, stored),
          spec,
        )}
      />
    </section>
  )
}

// The preview reads what the key holds until a row is actually edited: the
// selects prefill from the board's own suggestions, and serializing those would
// show a mapping nobody has written as though it were the stored value. Once
// edited it flips to what Save would write, said plainly so the two never read
// alike.
function groupingPreview(
  value: string,
  serialized: string,
  dirty: boolean,
  spec: MappingSpec,
): string {
  const shown = dirty ? serialized : value.trim()
  const text = shown === '' ? spec.emptyNote : shown
  return dirty ? `will write: ${text}` : text
}

function PinsBlock({
  ctx,
  spec,
  items,
  pinOptions,
}: {
  ctx: WriteContext
  spec: MappingSpec
  items: ConfigKey[]
  pinOptions: StatusPinOption[]
}) {
  const stored = useMemo(() => {
    const out: Record<string, string> = {}
    for (const item of items) out[item.key] = item.value
    return out
  }, [items])

  const {
    draft: pins,
    edit,
    note,
    save,
    saving,
    error,
  } = useBlockWrite({
    ctx,
    stored,
    signature: JSON.stringify(stored),
    write: async (draft: Record<string, string>) => {
      const written: string[] = []
      for (const item of items) {
        if ((draft[item.key] ?? '') === item.value) continue
        await writeConfig(ctx.repo, {
          key: item.key,
          value: draft[item.key] ?? '',
          layer: ctx.target,
        })
        written.push(item.key)
      }
      return written
    },
    savedNote: PIN_NOTE,
  })

  const changed = items.some((item) => pins[item.key] !== item.value)

  if (items.length === 0) return null

  return (
    <section className="flex flex-col gap-3">
      <MappingBlockHeader
        title="write pins"
        keyName="STATUS_*"
        description={spec.pinNote}
      />

      <PinFields
        keys={items.map((item) => item.key as PinKey)}
        values={pins}
        onChange={(key, next) => edit((prev) => ({ ...prev, [key]: next }))}
        pinOptions={pinOptions}
        badge={(key) => (
          <LayerChip
            layer={items.find((item) => item.key === key)?.layer ?? ''}
          />
        )}
      />

      <BlockFooter
        ctx={ctx}
        item={items[0]}
        onSave={save}
        saving={saving}
        disabled={!changed}
        note={note}
        error={error}
      />
    </section>
  )
}

function BlockFooter({
  ctx,
  item,
  onSave,
  saving,
  disabled,
  note,
  error,
  preview,
}: {
  ctx: WriteContext
  item: ConfigKey
  onSave: () => void
  saving: boolean
  disabled?: boolean
  note: string | null
  error: unknown
  preview?: string
}) {
  return (
    <div className="flex flex-col gap-2">
      {preview !== undefined && (
        <p className="break-all font-mono text-[0.7rem] text-faint">{preview}</p>
      )}
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <WriteTarget
          item={item}
          layers={ctx.layers}
          value={ctx.target}
          onChange={ctx.onTarget}
        />
        <span className="ml-auto">
          <Button
            size="sm"
            className="h-7 font-mono text-xs"
            onClick={onSave}
            disabled={saving || disabled}
          >
            <Check className="size-3.5" aria-hidden="true" />
            {saving ? 'Saving…' : 'Save'}
          </Button>
        </span>
      </div>
      <LayerHint target={ctx.target} hubRestart={ctx.hubRestart} />
      {note && (
        <p className="font-mono text-[0.7rem] text-done" role="status">
          {note}
        </p>
      )}
      {error ? <WriteError error={error} /> : null}
    </div>
  )
}
