import { Handle, Position, type NodeProps } from '@xyflow/react'

import { stepSize, type StepNode } from '@/lib/atlas-graph'
import { cn } from '@/lib/utils'

import { stepKindStyle } from './step-kinds'

export function StepNodeView({ data, selected }: NodeProps<StepNode>) {
  const kind = stepKindStyle(data.kind)
  const size = stepSize({ id: '', name: data.name, kind: data.kind })

  return (
    <div
      style={{ width: size.width, height: size.height }}
      className={cn(
        'atlas-node flex items-center gap-2.5 rounded-lg border px-3 shadow-sm transition-shadow',
        kind.bg,
        selected ? 'border-primary ring-2 ring-primary/40' : kind.border,
      )}
    >
      <Handle type="target" position={Position.Left} className="atlas-handle" />
      <span
        className={cn(
          'flex size-7 shrink-0 items-center justify-center rounded-md border bg-card',
          kind.border,
        )}
      >
        <kind.icon className={cn('size-4', kind.text)} aria-hidden="true" />
      </span>
      <span className="flex min-w-0 flex-col leading-tight">
        <span className="truncate font-sans text-sm font-medium text-foreground">
          {data.name}
        </span>
        <span
          className={cn(
            'font-mono text-[0.6rem] uppercase tracking-[0.16em]',
            kind.text,
          )}
        >
          {kind.label}
        </span>
      </span>
      <Handle
        type="source"
        position={Position.Right}
        className="atlas-handle"
      />
    </div>
  )
}
