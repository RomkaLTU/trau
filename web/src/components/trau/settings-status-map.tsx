import {
  useMemo,
  useState,
  type Dispatch,
  type ReactNode,
  type SetStateAction,
} from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, ChevronsUpDown, Plus, TriangleAlert } from 'lucide-react'

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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { StateGroupChip } from '@/components/trau/state-group-chip'
import {
  LayerChip,
  LayerHint,
  WriteError,
  WriteTarget,
  initialTarget,
} from '@/components/trau/settings-editor'
import { cn } from '@/lib/utils'
import { sectionLabel, type StateGroup } from '@/lib/backlog'
import { writeConfig, type ConfigKey } from '@/lib/config'
import {
  BOARD_STATES_KEY,
  MAPPABLE_GROUPS,
  PIN_KEYS,
  PIN_LABELS,
  STATUS_MAP_KEYS,
  UNMAPPED,
  boardNameError,
  deriveGroupingRows,
  serializeBoardStates,
  statusOptionsQueryOptions,
  toGroup,
  type GroupingRow,
  type StatusColumn,
  type StatusOptions,
  type StatusPinOption,
} from '@/lib/statusmap'

const GROUP_CHOICES: StateGroup[] = [...MAPPABLE_GROUPS, UNMAPPED]

const SYNC_NOTE =
  'Saved. The board regroups on the next sync pull for this repo.'

const PIN_NOTE = 'Saved. Pins apply to the next write this repo makes.'

const EMPTY_MAPPING = '(empty — grouping stays category-derived)'

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
  const boardItem = keys.find((item) => item.key === BOARD_STATES_KEY)

  if (!data || !boardItem?.editable) {
    return <>{keys.map(renderRow)}</>
  }
  return (
    <>
      <div className="border-b border-border/60 p-4">
        <StatusMapEditor
          repo={repo}
          keys={keys}
          options={data}
          layers={layers}
          hubRestart={hubRestart}
          onSaved={onSaved}
        />
      </div>
      {keys
        .filter((item) => !STATUS_MAP_KEYS.includes(item.key))
        .map(renderRow)}
    </>
  )
}

function StatusMapEditor({
  repo,
  keys,
  options,
  layers,
  hubRestart,
  onSaved,
}: {
  repo: string
  keys: ConfigKey[]
  options: StatusOptions
  layers: string[]
  hubRestart: boolean
  onSaved: (key: string, target: string, unset: boolean) => void
}) {
  const boardItem = useMemo(
    () => keys.find((item) => item.key === BOARD_STATES_KEY)!,
    [keys],
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
        item={boardItem}
        columns={options.error ? NO_COLUMNS : options.grouping}
        boardRead={!options.error}
      />
      <PinsBlock ctx={ctx} items={pinItems} pinOptions={options.pinOptions} />
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

function BlockHeader({
  title,
  keyName,
  item,
  description,
}: {
  title: string
  keyName: string
  item?: ConfigKey
  description: string
}) {
  return (
    <header className="flex flex-col gap-1.5">
      <div className="flex flex-wrap items-center gap-2">
        <h4 className="font-mono text-xs uppercase tracking-wider text-foreground">
          {title}
        </h4>
        <span className="font-mono text-[0.65rem] text-faint">{keyName}</span>
        {item && <LayerChip layer={item.layer} />}
      </div>
      <p className="text-xs leading-relaxed text-muted-foreground">
        {description}
      </p>
    </header>
  )
}

function GroupingBlock({
  ctx,
  item,
  columns,
  boardRead,
}: {
  ctx: WriteContext
  item: ConfigKey
  columns: StatusColumn[]
  boardRead: boolean
}) {
  const stored = useMemo(
    () => deriveGroupingRows(columns, item.value),
    [columns, item.value],
  )
  const [draftName, setDraftName] = useState('')
  const [draftGroup, setDraftGroup] = useState<StateGroup>('unstarted')
  const [nameError, setNameError] = useState<string | null>(null)

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
    signature: JSON.stringify([columns, item.value]),
    write: async (draft: GroupingRow[]) => {
      await writeConfig(ctx.repo, {
        key: BOARD_STATES_KEY,
        value: serializeBoardStates(draft),
        layer: ctx.target,
      })
      return [BOARD_STATES_KEY]
    },
    savedNote: SYNC_NOTE,
    onRestart: () => setNameError(null),
  })

  const setGroup = (name: string, group: StateGroup) => {
    edit((prev) =>
      prev.map((row) => (row.name === name ? { ...row, group } : row)),
    )
  }

  const addRow = () => {
    const invalid = boardNameError(draftName)
    if (invalid) {
      setNameError(invalid)
      return
    }
    const name = draftName.trim()
    if (rows.some((row) => row.name.toLowerCase() === name.toLowerCase())) {
      setNameError('That column is already listed.')
      return
    }
    edit((prev) => [
      ...prev,
      { name, group: draftGroup, suggested: UNMAPPED, onBoard: false },
    ])
    setDraftName('')
    setNameError(null)
  }

  const serialized = serializeBoardStates(rows)
  const unmapped = rows.filter((row) => row.group === UNMAPPED).length
  const exhaustive = serialized !== '' && unmapped > 0

  return (
    <section className="flex flex-col gap-3">
      <BlockHeader
        title="board column grouping"
        keyName={BOARD_STATES_KEY}
        item={item}
        description="Each Kanban column the team's boards carry, and the section trau files its work under. Leave every column unmapped to keep grouping by the state categories the project reports."
      />

      <div className="flex flex-col divide-y divide-border/60 rounded-md border border-border">
        {rows.length === 0 && (
          <p className="px-3 py-3 font-mono text-xs text-faint">
            no columns yet — add one below
          </p>
        )}
        {rows.map((row) => (
          <GroupingRowView
            key={row.name}
            row={row}
            boardRead={boardRead}
            exhaustive={exhaustive}
            onChange={(group) => setGroup(row.name, group)}
          />
        ))}
      </div>

      <div className="flex flex-col gap-1.5">
        <div className="flex flex-wrap items-center gap-2">
          <Input
            value={draftName}
            onChange={(e) => {
              setDraftName(e.target.value)
              setNameError(null)
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && !e.nativeEvent.isComposing) addRow()
            }}
            placeholder="column the board did not report"
            aria-label="New board column name"
            spellCheck={false}
            className="h-8 max-w-xs px-2 py-1 font-mono text-xs placeholder:text-faint"
          />
          <GroupSelect
            label="New board column group"
            value={draftGroup}
            onChange={setDraftGroup}
          />
          <Button
            variant="outline"
            size="sm"
            className="h-8 font-mono text-xs"
            onClick={addRow}
          >
            <Plus className="size-3.5" aria-hidden="true" />
            Add column
          </Button>
        </div>
        {nameError && (
          <p className="font-mono text-[0.7rem] text-fail" role="alert">
            {nameError}
          </p>
        )}
      </div>

      {exhaustive && (
        <p
          className="inline-flex items-start gap-2 rounded-md border border-warn/50 bg-warn/12 px-2.5 py-2 text-xs leading-relaxed text-warn"
          role="status"
        >
          <TriangleAlert
            className="mt-0.5 size-3.5 shrink-0"
            aria-hidden="true"
          />
          <span>
            {unmapped === 1
              ? '1 column stays unmapped'
              : `${unmapped} columns stay unmapped`}
            . A mapping that is set is exhaustive, so that work groups as{' '}
            {sectionLabel(UNMAPPED)}.
          </span>
        </p>
      )}

      <BlockFooter
        ctx={ctx}
        item={item}
        onSave={save}
        saving={saving}
        note={note}
        error={error}
        preview={groupingPreview(item.value, serialized, edited(rows, stored))}
      />
    </section>
  )
}

function edited(rows: GroupingRow[], stored: GroupingRow[]): boolean {
  return (
    rows.length !== stored.length ||
    rows.some(
      (row, i) => row.name !== stored[i].name || row.group !== stored[i].group,
    )
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
): string {
  const shown = dirty ? serialized : value.trim()
  const text = shown === '' ? EMPTY_MAPPING : shown
  return dirty ? `will write: ${text}` : text
}

function GroupingRowView({
  row,
  boardRead,
  exhaustive,
  onChange,
}: {
  row: GroupingRow
  boardRead: boolean
  exhaustive: boolean
  onChange: (group: StateGroup) => void
}) {
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-2 px-3 py-2">
      <span className="min-w-0 flex-1 truncate font-mono text-xs text-foreground">
        {row.name}
      </span>
      {boardRead && !row.onBoard && (
        <span className="font-mono text-[0.65rem] text-faint">
          not on the board
        </span>
      )}
      {exhaustive && row.group === UNMAPPED && (
        <span className="font-mono text-[0.65rem] text-warn">
          → {sectionLabel(UNMAPPED)}
        </span>
      )}
      <GroupSelect
        label={`${row.name} group`}
        value={row.group}
        onChange={onChange}
      />
    </div>
  )
}

function GroupSelect({
  label,
  value,
  onChange,
}: {
  label: string
  value: StateGroup
  onChange: (group: StateGroup) => void
}) {
  return (
    <Select value={value} onValueChange={(next) => onChange(toGroup(next))}>
      <SelectTrigger
        size="sm"
        aria-label={label}
        className="w-52 font-mono text-xs"
      >
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {GROUP_CHOICES.map((group) => (
          <SelectItem key={group} value={group}>
            <StateGroupChip group={group} />
            <span className="font-mono text-xs">{sectionLabel(group)}</span>
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

function PinsBlock({
  ctx,
  items,
  pinOptions,
}: {
  ctx: WriteContext
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
      <BlockHeader
        title="write pins"
        keyName="STATUS_*"
        description="The workflow status each lifecycle stage writes. A pin names a work-item state, never a board column — Azure DevOps refuses a board-column write. Leave one empty to resolve it from the workflow itself."
      />

      <div className="flex flex-col divide-y divide-border/60 rounded-md border border-border">
        {items.map((item) => (
          <div
            key={item.key}
            className="flex flex-wrap items-center gap-x-3 gap-y-2 px-3 py-2"
          >
            <span className="min-w-0 flex-1 truncate font-mono text-xs text-foreground">
              {PIN_LABELS[item.key as keyof typeof PIN_LABELS]}
            </span>
            <LayerChip layer={item.layer} />
            <StatusCombobox
              label={`${item.key} value`}
              value={pins[item.key] ?? ''}
              options={pinOptions}
              onChange={(next) =>
                edit((prev) => ({ ...prev, [item.key]: next }))
              }
            />
          </div>
        ))}
      </div>

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

function StatusCombobox({
  label,
  value,
  options,
  onChange,
}: {
  label: string
  value: string
  options: StatusPinOption[]
  onChange: (value: string) => void
}) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')

  const trimmed = query.trim()
  const custom =
    trimmed !== '' && !options.some((o) => o.name === trimmed) ? trimmed : null

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
          aria-label={label}
          className="h-8 w-56 justify-between font-mono text-xs"
        >
          <span className={cn('truncate', value === '' && 'text-faint')}>
            {value === '' ? '(resolve from the workflow)' : value}
          </span>
          <ChevronsUpDown className="size-3.5 shrink-0 text-muted-foreground" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-72 p-0">
        <Command>
          <CommandInput
            placeholder="Search or type a status name…"
            value={query}
            onValueChange={setQuery}
          />
          <CommandList>
            <CommandEmpty>No matching status.</CommandEmpty>
            <CommandGroup>
              <CommandItem value="__unset__" onSelect={() => commit('')}>
                <Check
                  className={cn('size-4', value === '' ? 'opacity-100' : 'opacity-0')}
                />
                <span className="flex-1 truncate font-mono text-xs text-muted-foreground">
                  (resolve from the workflow)
                </span>
              </CommandItem>
              {options.map((option) => (
                <CommandItem
                  key={option.name}
                  value={option.name}
                  onSelect={() => commit(option.name)}
                >
                  <Check
                    className={cn(
                      'size-4',
                      value === option.name ? 'opacity-100' : 'opacity-0',
                    )}
                  />
                  <span className="flex-1 truncate font-mono text-xs">
                    {option.name}
                  </span>
                  {option.category && (
                    <span className="font-mono text-[0.65rem] text-faint">
                      {option.category}
                    </span>
                  )}
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
