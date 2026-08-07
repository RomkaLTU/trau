import {
  useEffect,
  useMemo,
  useState,
  type Dispatch,
  type SetStateAction,
} from 'react'
import { useQuery } from '@tanstack/react-query'
import { ChevronDown, ChevronRight, RotateCw } from 'lucide-react'

import { Button } from '@/components/ui/button'
import {
  GroupingFields,
  MappingBlockHeader,
  PinFields,
} from '@/components/trau/status-map-editor'
import {
  PIN_KEYS,
  deriveGroupingRows,
  groupingEdited,
  probeStatusOptions,
  serializeGrouping,
  type GroupingRow,
  type MappingSpec,
  type StatusOptionsProbe,
} from '@/lib/statusmap'
import { Callout, FieldLabel, Hint } from './ui'

// The optional mapping section of the tracker step. It costs nothing to skip: the
// rows arrive prefilled with the provider's own suggestions and none of them is
// written until one is actually changed. The parent owns the values because it
// owns the write, and remounts this component whenever the provider or the
// binding moves — a fetch made under one binding says nothing about another.
export function TrackerMappingSection({
  provider,
  spec,
  probe,
  onChange,
}: {
  provider: string
  spec: MappingSpec
  probe: StatusOptionsProbe
  onChange: (values: Record<string, string>) => void
}) {
  const [expanded, setExpanded] = useState(false)
  const [rows, setRows] = useState<GroupingRow[] | null>(null)
  const [pins, setPins] = useState<Record<string, string>>({})

  // Nothing is fetched until the section is opened, and nothing is cached: the
  // credentials behind an answer only live in the form, so a kept read would
  // outlive the state it was true for.
  const query = useQuery({
    queryKey: ['wizard-status-options', provider, probe.binding],
    queryFn: () => probeStatusOptions(provider, probe),
    enabled: expanded,
    retry: false,
    gcTime: 0,
    staleTime: Infinity,
  })

  const options = query.data ?? null
  const readable = options !== null && !options.error

  // The rows the fetch prefilled, kept alongside the draft so an untouched row
  // reads as the suggestion it is rather than as a choice somebody made.
  const prefilled = useMemo(
    () => (readable ? deriveGroupingRows(options.grouping, '', spec) : null),
    [readable, options, spec],
  )
  if (prefilled !== null && rows === null) setRows(prefilled)

  // The draft only exists once the fetch has prefilled it, so an earlier update
  // is dropped rather than seeding rows the provider never reported.
  const editRows: Dispatch<SetStateAction<GroupingRow[]>> = (next) =>
    setRows((prev) => {
      if (prev === null) return prev
      return typeof next === 'function' ? next(prev) : next
    })

  const values = useMemo(() => {
    const out: Record<string, string> = {}
    if (rows !== null && prefilled !== null && groupingEdited(rows, prefilled)) {
      const serialized = serializeGrouping(rows, spec)
      if (serialized !== '') out[spec.key] = serialized
    }
    for (const key of PIN_KEYS) {
      const pinned = (pins[key] ?? '').trim()
      if (pinned !== '') out[key] = pinned
    }
    return out
  }, [rows, prefilled, pins, spec])

  useEffect(() => onChange(values), [values, onChange])

  const failure = query.error
    ? (query.error as Error).message
    : options?.error
      ? options.error
      : query.data === null
        ? 'This tracker has no status-mapping options.'
        : null

  return (
    <div className="flex flex-col gap-3 rounded-md border border-border bg-background/40 p-4">
      <button
        type="button"
        aria-expanded={expanded}
        onClick={() => setExpanded((open) => !open)}
        className="flex items-start gap-2 text-left"
      >
        {expanded ? (
          <ChevronDown className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
        ) : (
          <ChevronRight className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
        )}
        <span className="flex flex-col gap-1">
          <FieldLabel>status mapping (optional)</FieldLabel>
          <Hint>
            Which {spec.nounPlural} count as backlog, in progress or done, and the status each
            lifecycle stage writes. Skipping this costs nothing — trau derives both from the
            tracker's own metadata, and you can change it later in settings.
          </Hint>
        </span>
        {Object.keys(values).length > 0 && (
          <span className="ml-auto shrink-0 rounded-full border border-teal/50 bg-teal/10 px-1.5 py-0.5 font-mono text-[0.6rem] text-teal">
            {Object.keys(values).length} set
          </span>
        )}
      </button>

      {expanded && (
        <div className="flex flex-col gap-6 pl-[1.375rem]">
          {query.isFetching && (
            <p className="font-mono text-xs text-muted-foreground">
              Reading the {spec.nounPlural}…
            </p>
          )}

          {failure !== null && (
            <Callout
              tone="fail"
              title={`Couldn't read the ${spec.nounPlural}`}
              actions={
                <Button type="button" variant="outline" size="sm" onClick={() => query.refetch()}>
                  <RotateCw className="size-3.5" aria-hidden="true" />
                  Try again
                </Button>
              }
            >
              {failure}
              {options?.hint && (
                <span className="mt-1 block">{options.hint}</span>
              )}
              <span className="mt-1 block">
                Nothing is blocked — continue without a mapping and set one from the repo's
                settings once it is registered.
              </span>
            </Callout>
          )}

          {readable && rows !== null && (
            <>
              <section className="flex flex-col gap-3">
                <MappingBlockHeader
                  title={spec.title}
                  keyName={spec.key}
                  description={spec.description}
                />
                <GroupingFields
                  spec={spec}
                  rows={rows}
                  onRows={editRows}
                  boardRead
                />
              </section>

              <section className="flex flex-col gap-3">
                <MappingBlockHeader
                  title="write pins"
                  keyName="STATUS_*"
                  description={spec.pinNote}
                />
                <PinFields
                  keys={PIN_KEYS}
                  values={pins}
                  onChange={(key, next) => setPins((prev) => ({ ...prev, [key]: next }))}
                  pinOptions={options.pinOptions}
                />
              </section>
            </>
          )}
        </div>
      )}
    </div>
  )
}
