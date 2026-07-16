import { Handle, Position, type NodeProps } from '@xyflow/react'
import { KeyRound } from 'lucide-react'

import { entitySize, type EntityNode } from '@/lib/atlas-graph'
import { cn } from '@/lib/utils'

export function EntityNodeView({ data, selected }: NodeProps<EntityNode>) {
  const size = entitySize({
    id: '',
    name: data.name,
    domain: data.domain,
    fields: data.fields,
  })

  return (
    <div
      style={{ width: size.width, height: size.height }}
      className={cn(
        'atlas-node flex flex-col overflow-hidden rounded-lg border bg-card text-left shadow-sm transition-shadow',
        selected ? 'border-primary ring-2 ring-primary/40' : 'border-border',
      )}
    >
      <Handle type="target" position={Position.Left} className="atlas-handle" />
      <div className="flex items-center justify-between gap-2 border-b border-border bg-secondary/40 px-3 py-2">
        <span className="truncate font-mono text-sm font-medium text-foreground">
          {data.name}
        </span>
        {data.domain && (
          <span className="shrink-0 font-mono text-[0.6rem] uppercase tracking-[0.16em] text-muted-foreground">
            {data.domain}
          </span>
        )}
      </div>
      <ul className="flex flex-col">
        {(data.fields ?? []).map((field) => (
          <li
            key={field.name}
            className="flex h-[26px] items-center gap-2 px-3 font-mono text-xs"
          >
            <KeyRound
              className={cn(
                'size-3 shrink-0',
                field.pk ? 'text-primary' : 'text-transparent',
              )}
              aria-hidden="true"
            />
            <span
              className={cn(
                'truncate',
                field.pk
                  ? 'font-medium text-foreground'
                  : 'text-muted-foreground',
              )}
            >
              {field.name}
            </span>
            <span className="ml-auto truncate text-faint">{field.type}</span>
          </li>
        ))}
      </ul>
      <Handle
        type="source"
        position={Position.Right}
        className="atlas-handle"
      />
    </div>
  )
}
