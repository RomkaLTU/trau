import type { Edge, Node } from '@xyflow/react'

import type {
  AppFlows,
  DataModel,
  Entity,
  Field,
  Flow,
  Step,
  StepKind,
} from './atlas'

export type EntityNodeData = {
  name: string
  domain: string
  fields: Field[]
}

export type StepNodeData = {
  name: string
  kind: StepKind
}

export type DomainNodeData = {
  domain: string
}

export type EntityNode = Node<EntityNodeData, 'entity'>
export type StepNode = Node<StepNodeData, 'step'>
export type DomainNode = Node<DomainNodeData, 'domain'>
export type AtlasNode = EntityNode | StepNode | DomainNode

export interface AtlasGraph {
  nodes: AtlasNode[]
  edges: Edge[]
}

const ENTITY_WIDTH = 240
const ENTITY_HEADER = 46
const ENTITY_ROW = 26
const ENTITY_FOOTER = 10
const STEP_HEIGHT = 58

export function entitySize(entity: Entity): { width: number; height: number } {
  return {
    width: ENTITY_WIDTH,
    height:
      ENTITY_HEADER + (entity.fields ?? []).length * ENTITY_ROW + ENTITY_FOOTER,
  }
}

export function stepSize(step: Step): { width: number; height: number } {
  return {
    width: Math.max(150, Math.min(280, step.name.length * 8 + 76)),
    height: STEP_HEIGHT,
  }
}

const domainNodeId = (domain: string) => `domain::${domain}`

function distinctDomains(entities: Entity[]): string[] {
  const seen = new Set<string>()
  const order: string[] = []
  for (const e of entities) {
    if (!seen.has(e.domain)) {
      seen.add(e.domain)
      order.push(e.domain)
    }
  }
  return order
}

// dataModelToGraph maps a Data model document to React Flow nodes and edges.
// Entity ids become node ids verbatim so a regenerated document with stable ids
// updates in place; entities are nested under a group node per domain for
// clustered layout, and relationships become cardinality-labelled edges.
export function dataModelToGraph(doc: DataModel): AtlasGraph {
  const groups: DomainNode[] = distinctDomains(doc.entities).map((domain) => ({
    id: domainNodeId(domain),
    type: 'domain',
    position: { x: 0, y: 0 },
    selectable: false,
    draggable: false,
    data: { domain },
  }))

  const entities: EntityNode[] = doc.entities.map((entity) => {
    const size = entitySize(entity)
    return {
      id: entity.id,
      type: 'entity',
      position: { x: 0, y: 0 },
      parentId: domainNodeId(entity.domain),
      width: size.width,
      height: size.height,
      data: { name: entity.name, domain: entity.domain, fields: entity.fields },
    }
  })

  const edges: Edge[] = doc.relationships.map((rel) => ({
    id: rel.id,
    source: rel.from,
    target: rel.to,
    label: rel.cardinality,
    data: { detail: rel.label },
  }))

  return { nodes: [...groups, ...entities], edges }
}

// flowToGraph maps one Flow to React Flow nodes and edges. Step ids become node
// ids verbatim; edges carry the transition label and a synthesised unique id
// since the document does not id them.
export function flowToGraph(flow: Flow): AtlasGraph {
  const nodes: StepNode[] = flow.steps.map((step) => {
    const size = stepSize(step)
    return {
      id: step.id,
      type: 'step',
      position: { x: 0, y: 0 },
      width: size.width,
      height: size.height,
      data: { name: step.name, kind: step.kind },
    }
  })

  const edges: Edge[] = flow.edges.map((edge, i) => ({
    id: `${edge.from}__${edge.to}__${i}`,
    source: edge.from,
    target: edge.to,
    label: edge.label || undefined,
  }))

  return { nodes, edges }
}

export function asDataModel(document: unknown): DataModel {
  return document as DataModel
}

export function asAppFlows(document: unknown): AppFlows {
  return document as AppFlows
}
