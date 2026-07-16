import { useMemo, useState } from 'react'

import type { AppFlows } from '@/lib/atlas'
import { flowToGraph } from '@/lib/atlas-graph'
import { cn } from '@/lib/utils'

import { GraphCanvas } from './graph-canvas'
import { stepKindStyle } from './step-kinds'

export function AppFlowsView({ doc }: { doc: AppFlows }) {
  const [flowId, setFlowId] = useState(doc.flows[0]?.id ?? '')
  const active = doc.flows.find((f) => f.id === flowId) ?? doc.flows[0]
  const graph = useMemo(
    () => (active ? flowToGraph(active) : { nodes: [], edges: [] }),
    [active],
  )

  const kinds = useMemo(
    () => (active ? Array.from(new Set(active.steps.map((s) => s.kind))) : []),
    [active],
  )

  if (!active) {
    return (
      <p className="font-mono text-sm text-muted-foreground">
        This View has no flows.
      </p>
    )
  }

  return (
    <div className="flex flex-1 flex-col gap-3">
      <div role="tablist" aria-label="Flows" className="flex flex-wrap gap-1.5">
        {doc.flows.map((flow) => {
          const selected = flow.id === active.id
          return (
            <button
              key={flow.id}
              type="button"
              role="tab"
              aria-selected={selected}
              onClick={() => setFlowId(flow.id)}
              className={cn(
                'rounded-md border px-3 py-1 font-mono text-xs transition-colors',
                selected
                  ? 'border-primary/50 bg-primary/10 text-primary'
                  : 'border-border text-muted-foreground hover:text-foreground',
              )}
            >
              {flow.name}
            </button>
          )
        })}
      </div>

      {active.summary && (
        <p className="text-pretty text-sm leading-relaxed text-muted-foreground">
          {active.summary}
        </p>
      )}

      <div className="flex flex-wrap gap-x-3 gap-y-1">
        {kinds.map((kind) => {
          const style = stepKindStyle(kind)
          return (
            <span
              key={kind}
              className="inline-flex items-center gap-1.5 font-mono text-[0.65rem] uppercase tracking-[0.12em] text-muted-foreground"
            >
              <span
                aria-hidden="true"
                className="size-2 rounded-full"
                style={{ backgroundColor: style.swatch }}
              />
              {style.label}
            </span>
          )
        })}
      </div>

      <GraphCanvas
        graph={graph}
        direction="RIGHT"
        fitKey={`flow:${active.id}`}
        emptyLabel="This flow has no steps."
      />
    </div>
  )
}
