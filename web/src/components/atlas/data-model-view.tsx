import { useMemo } from 'react'

import type { DataModel } from '@/lib/atlas'
import { dataModelToGraph } from '@/lib/atlas-graph'

import { GraphCanvas } from './graph-canvas'

export function DataModelView({ doc }: { doc: DataModel }) {
  const graph = useMemo(() => dataModelToGraph(doc), [doc])
  const domains = useMemo(
    () => Array.from(new Set(doc.entities.map((e) => e.domain))),
    [doc.entities],
  )

  return (
    <div className="flex flex-1 flex-col gap-3">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 font-mono text-xs text-muted-foreground">
        <span>
          {doc.entities.length} entities · {doc.relationships.length}{' '}
          relationships · {domains.length} domains
        </span>
        <span className="text-faint">
          click a node to trace its relationships
        </span>
      </div>
      <GraphCanvas
        graph={graph}
        direction="RIGHT"
        fitKey="data-model"
        emptyLabel="This data model has no entities."
      />
    </div>
  )
}
