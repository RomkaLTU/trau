import { useCallback, useEffect, useMemo, useState } from 'react'
import { createPortal } from 'react-dom'
import {
  Background,
  Controls,
  MarkerType,
  MiniMap,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
  type ColorMode,
  type Edge,
  type Node,
  type NodeMouseHandler,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import { Maximize2, Minimize2 } from 'lucide-react'

import type { AtlasGraph, AtlasNode, StepNodeData } from '@/lib/atlas-graph'
import { layoutGraph, type LayoutDirection } from '@/lib/atlas-layout'
import { cn } from '@/lib/utils'

import { DomainNodeView } from './domain-node'
import { EntityNodeView } from './entity-node'
import { StepNodeView } from './step-node'
import { stepKindStyle } from './step-kinds'

const NODE_TYPES = {
  entity: EntityNodeView,
  step: StepNodeView,
  domain: DomainNodeView,
}

const DEFAULT_EDGE_OPTIONS = {
  type: 'default',
  markerEnd: {
    type: MarkerType.ArrowClosed,
    width: 16,
    height: 16,
    color: 'var(--color-faint)',
  },
  style: { stroke: 'var(--color-faint)' },
  labelStyle: {
    fill: 'var(--color-muted-foreground)',
    fontFamily: 'var(--font-mono)',
    fontSize: 11,
  },
  labelBgStyle: { fill: 'var(--color-card)' },
  labelBgPadding: [6, 2] as [number, number],
  labelBgBorderRadius: 4,
}

interface Neighbours {
  nodes: Set<string>
  edges: Set<string>
}

function neighboursOf(
  selected: string | null,
  edges: Edge[],
): Neighbours | null {
  if (!selected) return null
  const nodes = new Set<string>([selected])
  const touched = new Set<string>()
  for (const edge of edges) {
    if (edge.source === selected || edge.target === selected) {
      touched.add(edge.id)
      nodes.add(edge.source)
      nodes.add(edge.target)
    }
  }
  return { nodes, edges: touched }
}

function readColorMode(): ColorMode {
  return globalThis.document?.documentElement.classList.contains('dark')
    ? 'dark'
    : 'light'
}

function useResolvedColorMode(): ColorMode {
  const [colorMode, setColorMode] = useState<ColorMode>(readColorMode)
  useEffect(() => {
    const root = globalThis.document?.documentElement
    if (!root) return
    const observer = new MutationObserver(() => setColorMode(readColorMode()))
    observer.observe(root, { attributes: true, attributeFilter: ['class'] })
    return () => observer.disconnect()
  }, [])
  return colorMode
}

export interface GraphCanvasProps {
  graph: AtlasGraph
  direction: LayoutDirection
  fitKey: string
  emptyLabel?: string
}

export function GraphCanvas(props: GraphCanvasProps) {
  return (
    <ReactFlowProvider>
      <CanvasInner {...props} />
    </ReactFlowProvider>
  )
}

function CanvasInner({
  graph,
  direction,
  fitKey,
  emptyLabel,
}: GraphCanvasProps) {
  const colorMode = useResolvedColorMode()
  const { fitView } = useReactFlow()

  const [laidNodes, setLaidNodes] = useState<AtlasNode[]>([])
  const [ready, setReady] = useState(false)
  const [selected, setSelected] = useState<string | null>(null)
  const [fullscreen, setFullscreen] = useState(false)

  useEffect(() => {
    let cancelled = false
    setReady(false)
    setSelected(null)
    layoutGraph(graph.nodes, graph.edges, direction)
      .then((nodes) => {
        if (!cancelled) {
          setLaidNodes(nodes)
          setReady(true)
        }
      })
      .catch(() => {
        if (!cancelled) {
          setLaidNodes(graph.nodes)
          setReady(true)
        }
      })
    return () => {
      cancelled = true
    }
  }, [graph, direction])

  useEffect(() => {
    if (!ready) return
    const timer = setTimeout(
      () => fitView({ padding: 0.18, duration: 300 }),
      60,
    )
    return () => clearTimeout(timer)
  }, [ready, fitView, laidNodes, fullscreen])

  useEffect(() => {
    if (!fullscreen) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setFullscreen(false)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [fullscreen])

  const neighbours = useMemo(
    () => neighboursOf(selected, graph.edges),
    [selected, graph.edges],
  )

  const displayNodes = useMemo(
    () =>
      laidNodes.map((node) => ({
        ...node,
        selected: node.id === selected,
        className: cn(
          node.className,
          neighbours &&
            node.type !== 'domain' &&
            !neighbours.nodes.has(node.id) &&
            'atlas-dim',
        ),
      })),
    [laidNodes, selected, neighbours],
  )

  const displayEdges = useMemo(
    () =>
      graph.edges.map((edge) => ({
        ...edge,
        animated: neighbours ? neighbours.edges.has(edge.id) : false,
        className: neighbours
          ? neighbours.edges.has(edge.id)
            ? 'atlas-edge-active'
            : 'atlas-dim'
          : undefined,
      })),
    [graph.edges, neighbours],
  )

  const onNodeClick = useCallback<NodeMouseHandler>((_, node) => {
    if (node.type === 'domain') return
    setSelected((current) => (current === node.id ? null : node.id))
  }, [])

  const minimapColor = useCallback((node: Node) => {
    if (node.type === 'entity') return 'var(--color-primary)'
    if (node.type === 'step')
      return stepKindStyle((node.data as StepNodeData).kind).swatch
    return 'transparent'
  }, [])

  const canvas = (
    <div
      className={cn(
        'atlas-canvas relative',
        fullscreen
          ? 'fixed inset-0 z-[100] bg-background'
          : 'h-[calc(100vh-14rem)] min-h-[520px] overflow-hidden rounded-lg border border-border bg-card',
      )}
    >
      <ReactFlow
        key={`${fitKey}:${colorMode}`}
        nodes={displayNodes}
        edges={displayEdges}
        nodeTypes={NODE_TYPES}
        colorMode={colorMode}
        defaultEdgeOptions={DEFAULT_EDGE_OPTIONS}
        onNodeClick={onNodeClick}
        onPaneClick={() => setSelected(null)}
        nodesDraggable={false}
        nodesConnectable={false}
        selectNodesOnDrag={false}
        edgesFocusable={false}
        minZoom={0.1}
        fitView
        fitViewOptions={{ padding: 0.18 }}
        proOptions={{ hideAttribution: true }}
      >
        <Background gap={22} color="var(--color-border)" />
        <MiniMap
          pannable
          zoomable
          nodeColor={minimapColor}
          className="!bottom-3 !right-3 rounded-md border border-border"
          maskColor="color-mix(in oklab, var(--color-background) 70%, transparent)"
        />
        <Controls className="atlas-controls" showInteractive={false} />
      </ReactFlow>

      {!ready && (
        <div className="pointer-events-none absolute inset-0 flex items-center justify-center font-mono text-sm text-muted-foreground">
          Laying out…
        </div>
      )}
      {ready && laidNodes.length === 0 && emptyLabel && (
        <div className="pointer-events-none absolute inset-0 flex items-center justify-center font-mono text-sm text-muted-foreground">
          {emptyLabel}
        </div>
      )}

      <button
        type="button"
        onClick={() => setFullscreen((v) => !v)}
        title={fullscreen ? 'Exit fullscreen (Esc)' : 'Fullscreen'}
        className="absolute right-3 top-3 z-10 inline-flex items-center gap-1.5 rounded-md border border-border bg-card/90 px-2.5 py-1.5 font-mono text-xs text-muted-foreground shadow-sm backdrop-blur transition-colors hover:text-foreground"
      >
        {fullscreen ? (
          <Minimize2 className="size-3.5" />
        ) : (
          <Maximize2 className="size-3.5" />
        )}
        {fullscreen ? 'Exit' : 'Fullscreen'}
      </button>
    </div>
  )

  return fullscreen ? createPortal(canvas, document.body) : canvas
}
