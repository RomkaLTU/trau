import type { NodeProps } from '@xyflow/react'

import type { DomainNode } from '@/lib/atlas-graph'

export function DomainNodeView({ data }: NodeProps<DomainNode>) {
  return (
    <div className="atlas-domain pointer-events-none relative size-full rounded-2xl border border-dashed border-border/70 bg-muted/20">
      <span className="absolute left-3 top-2 font-mono text-[0.65rem] uppercase tracking-[0.2em] text-muted-foreground">
        {data.domain || 'general'}
      </span>
    </div>
  )
}
