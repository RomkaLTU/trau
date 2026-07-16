import ELK, { type ElkNode } from 'elkjs/lib/elk.bundled.js'
import type { Edge } from '@xyflow/react'

import type { AtlasNode } from './atlas-graph'

const elk = new ELK()

export type LayoutDirection = 'RIGHT' | 'DOWN'

const baseOptions = (direction: LayoutDirection): Record<string, string> => ({
  'elk.algorithm': 'layered',
  'elk.direction': direction,
  'elk.hierarchyHandling': 'INCLUDE_CHILDREN',
  'elk.layered.spacing.nodeNodeBetweenLayers': '96',
  'elk.spacing.nodeNode': '44',
  'elk.spacing.componentComponent': '64',
  'elk.layered.considerModelOrder.strategy': 'NODES_AND_EDGES',
})

// layoutGraph runs an elkjs layered layout over the atlas nodes and returns them
// with computed positions. Nodes carrying a parentId are laid out inside their
// group container (domain clustering); positions come back relative to the parent,
// exactly as React Flow expects for nested nodes.
export async function layoutGraph(
  nodes: AtlasNode[],
  edges: Edge[],
  direction: LayoutDirection,
): Promise<AtlasNode[]> {
  if (nodes.length === 0) return nodes

  const childrenOf = new Map<string, AtlasNode[]>()
  const roots: AtlasNode[] = []
  for (const node of nodes) {
    if (node.parentId) {
      const siblings = childrenOf.get(node.parentId) ?? []
      siblings.push(node)
      childrenOf.set(node.parentId, siblings)
    } else {
      roots.push(node)
    }
  }

  const toElk = (node: AtlasNode): ElkNode => {
    const children = childrenOf.get(node.id)
    if (children && children.length > 0) {
      return {
        id: node.id,
        layoutOptions: { 'elk.padding': '[top=42,left=18,bottom=18,right=18]' },
        children: children.map(toElk),
      }
    }
    return {
      id: node.id,
      width: node.width ?? 160,
      height: node.height ?? 60,
    }
  }

  const laidOut = await elk.layout({
    id: 'root',
    layoutOptions: baseOptions(direction),
    children: roots.map(toElk),
    edges: edges.map((edge) => ({
      id: edge.id,
      sources: [edge.source],
      targets: [edge.target],
    })),
  })

  const positions = new Map<string, ElkNode>()
  const collect = (elkNodes: ElkNode[] | undefined) => {
    for (const node of elkNodes ?? []) {
      positions.set(node.id, node)
      collect(node.children)
    }
  }
  collect(laidOut.children)

  return nodes.map((node) => {
    const laid = positions.get(node.id)
    if (!laid) return node
    const next: AtlasNode = {
      ...node,
      position: { x: laid.x ?? 0, y: laid.y ?? 0 },
    }
    if (node.type === 'domain') {
      next.width = laid.width
      next.height = laid.height
      next.style = { ...node.style, width: laid.width, height: laid.height }
    }
    return next
  })
}
