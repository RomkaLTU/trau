import { useState, type Dispatch, type ReactNode, type SetStateAction } from 'react'
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
import { cn } from '@/lib/utils'
import { sectionLabel, type StateGroup } from '@/lib/backlog'
import {
  MAPPABLE_GROUPS,
  PIN_LABELS,
  UNMAPPED,
  boardNameError,
  conditionallyDerived,
  serializeGrouping,
  toGroup,
  type GroupingRow,
  type MappingSpec,
  type PinKey,
  type StatusPinOption,
} from '@/lib/statusmap'

const GROUP_CHOICES: StateGroup[] = [...MAPPABLE_GROUPS, UNMAPPED]
const OVERLAY_GROUP_CHOICES: StateGroup[] = [...MAPPABLE_GROUPS]

// The controls a status mapping is edited with, and nothing about where the rows
// came from or what saving them means: the settings page feeds them a repo's
// stored key and the onboarding wizard an in-form draft.
export function MappingBlockHeader({
  title,
  keyName,
  badge,
  description,
}: {
  title: string
  keyName: string
  badge?: ReactNode
  description: string
}) {
  return (
    <header className="flex flex-col gap-1.5">
      <div className="flex flex-wrap items-center gap-2">
        <h4 className="font-mono text-xs uppercase tracking-wider text-foreground">
          {title}
        </h4>
        <span className="font-mono text-[0.65rem] text-faint">{keyName}</span>
        {badge}
      </div>
      <p className="text-xs leading-relaxed text-muted-foreground">
        {description}
      </p>
    </header>
  )
}

export function GroupingFields({
  spec,
  rows,
  onRows,
  boardRead,
}: {
  spec: MappingSpec
  rows: GroupingRow[]
  onRows: Dispatch<SetStateAction<GroupingRow[]>>
  boardRead: boolean
}) {
  const [draftName, setDraftName] = useState('')
  const [draftGroup, setDraftGroup] = useState<StateGroup>('unstarted')
  const [nameError, setNameError] = useState<string | null>(null)

  const setGroup = (name: string, group: StateGroup) => {
    onRows((prev) =>
      prev.map((row) => (row.name === name ? { ...row, group } : row)),
    )
  }

  const setMapped = (name: string, mapped: boolean) => {
    onRows((prev) =>
      prev.map((row) => (row.name === name ? { ...row, mapped } : row)),
    )
  }

  const addRow = () => {
    const invalid = boardNameError(draftName, spec.noun)
    if (invalid) {
      setNameError(invalid)
      return
    }
    const name = draftName.trim()
    if (rows.some((row) => row.name.toLowerCase() === name.toLowerCase())) {
      setNameError(`That ${spec.noun} is already listed.`)
      return
    }
    onRows((prev) => [
      ...prev,
      {
        name,
        group: draftGroup,
        suggested: UNMAPPED,
        onBoard: false,
        mapped: true,
      },
    ])
    setDraftName('')
    setNameError(null)
  }

  const unmapped = rows.filter((row) => row.group === UNMAPPED).length
  const exhaustive =
    !spec.overlay && serializeGrouping(rows, spec) !== '' && unmapped > 0

  return (
    <>
      <div className="flex flex-col divide-y divide-border/60 rounded-md border border-border">
        {rows.length === 0 && (
          <p className="px-3 py-3 font-mono text-xs text-faint">
            no {spec.nounPlural} yet — add one below
          </p>
        )}
        {rows.map((row) => (
          <GroupingRowView
            key={row.name}
            row={row}
            spec={spec}
            boardRead={boardRead}
            exhaustive={exhaustive}
            onChange={(group) => setGroup(row.name, group)}
            onMapped={(mapped) => setMapped(row.name, mapped)}
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
            placeholder={spec.placeholder}
            aria-label={`New ${spec.noun} name`}
            spellCheck={false}
            className="h-8 max-w-xs px-2 py-1 font-mono text-xs placeholder:text-faint"
          />
          <GroupSelect
            label={`New ${spec.noun} group`}
            spec={spec}
            value={draftGroup}
            onChange={setDraftGroup}
          />
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="h-8 font-mono text-xs"
            onClick={addRow}
          >
            <Plus className="size-3.5" aria-hidden="true" />
            {spec.addLabel}
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
              ? `1 ${spec.noun} stays unmapped`
              : `${unmapped} ${spec.nounPlural} stay unmapped`}
            . A mapping that is set is exhaustive, so that work groups as{' '}
            {sectionLabel(UNMAPPED)}.
          </span>
        </p>
      )}
    </>
  )
}

function GroupingRowView({
  row,
  spec,
  boardRead,
  exhaustive,
  onChange,
  onMapped,
}: {
  row: GroupingRow
  spec: MappingSpec
  boardRead: boolean
  exhaustive: boolean
  onChange: (group: StateGroup) => void
  onMapped: (mapped: boolean) => void
}) {
  const overridden = spec.overlay && row.group !== row.suggested
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
      {overridden && (
        <span className="font-mono text-[0.65rem] text-faint">overridden</span>
      )}
      {!overridden && conditionallyDerived(row, spec) && (
        <MappedToggle row={row} spec={spec} onToggle={onMapped} />
      )}
      <GroupSelect
        label={`${row.name} group`}
        spec={spec}
        value={row.group}
        onChange={onChange}
      />
    </div>
  )
}

// A row whose section the provider derives only conditionally carries a choice
// the select cannot express: the same section, but named by hand so the nuance
// behind the suggestion stops moving part of its work elsewhere. This is that
// choice, and the only place a mapping equal to the suggestion is visible at all.
function MappedToggle({
  row,
  spec,
  onToggle,
}: {
  row: GroupingRow
  spec: MappingSpec
  onToggle: (mapped: boolean) => void
}) {
  return (
    <button
      type="button"
      aria-pressed={row.mapped}
      aria-label={`Map ${row.name} by hand`}
      title={
        row.mapped
          ? `Mapped by hand — every issue in this ${spec.noun} groups under ${sectionLabel(row.group)}.`
          : `Derived — some issues in this ${spec.noun} still group elsewhere. Map it by hand to hold them all under ${sectionLabel(row.group)}.`
      }
      onClick={() => onToggle(!row.mapped)}
      className={cn(
        'rounded border px-1.5 py-0.5 font-mono text-[0.65rem] transition-colors',
        row.mapped
          ? 'border-border bg-secondary/60 text-foreground'
          : 'border-transparent text-faint hover:border-border/60 hover:text-muted-foreground',
      )}
    >
      {row.mapped ? 'mapped' : 'derived'}
    </button>
  )
}

function GroupSelect({
  label,
  spec,
  value,
  onChange,
}: {
  label: string
  spec: MappingSpec
  value: StateGroup
  onChange: (group: StateGroup) => void
}) {
  // An overlay row is always governed by something, so Unknown is not offered —
  // except to a row already sitting on it, which would otherwise render blank.
  const choices =
    spec.overlay && value !== UNMAPPED ? OVERLAY_GROUP_CHOICES : GROUP_CHOICES
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
        {choices.map((group) => (
          <SelectItem key={group} value={group}>
            <StateGroupChip group={group} />
            <span className="font-mono text-xs">{sectionLabel(group)}</span>
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

export function PinFields({
  keys,
  values,
  onChange,
  pinOptions,
  badge,
}: {
  keys: readonly PinKey[]
  values: Record<string, string>
  onChange: (key: PinKey, value: string) => void
  pinOptions: StatusPinOption[]
  badge?: (key: PinKey) => ReactNode
}) {
  return (
    <div className="flex flex-col divide-y divide-border/60 rounded-md border border-border">
      {keys.map((key) => (
        <div
          key={key}
          className="flex flex-wrap items-center gap-x-3 gap-y-2 px-3 py-2"
        >
          <span className="min-w-0 flex-1 truncate font-mono text-xs text-foreground">
            {PIN_LABELS[key]}
          </span>
          {badge?.(key)}
          <StatusCombobox
            label={`${key} value`}
            value={values[key] ?? ''}
            options={pinOptions}
            onChange={(next) => onChange(key, next)}
          />
        </div>
      ))}
    </div>
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
          type="button"
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
