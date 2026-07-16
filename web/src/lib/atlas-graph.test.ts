import { describe, expect, it } from 'vitest'

import { appFlowsFixture, dataModelFixture } from './atlas-fixtures'
import { asDataModel, dataModelToGraph, flowToGraph } from './atlas-graph'

describe('dataModelToGraph', () => {
  const graph = dataModelToGraph(dataModelFixture)
  const entities = graph.nodes.filter((n) => n.type === 'entity')
  const groups = graph.nodes.filter((n) => n.type === 'domain')

  it('uses entity ids as node ids verbatim', () => {
    const ids = entities.map((n) => n.id)
    expect(ids).toEqual(dataModelFixture.entities.map((e) => e.id))
  })

  it('nests each entity under a group node for its domain', () => {
    const distinct = new Set(dataModelFixture.entities.map((e) => e.domain))
    expect(groups).toHaveLength(distinct.size)
    for (const entity of dataModelFixture.entities) {
      const node = entities.find((n) => n.id === entity.id)
      expect(node?.parentId).toBe(`domain::${entity.domain}`)
      expect(groups.some((g) => g.id === node?.parentId)).toBe(true)
    }
  })

  it('orders every group node before its children', () => {
    for (const entity of entities) {
      const parentIndex = graph.nodes.findIndex((n) => n.id === entity.parentId)
      const childIndex = graph.nodes.findIndex((n) => n.id === entity.id)
      expect(parentIndex).toBeGreaterThanOrEqual(0)
      expect(parentIndex).toBeLessThan(childIndex)
    }
  })

  it('maps relationships to cardinality-labelled edges keyed by id', () => {
    expect(graph.edges).toHaveLength(dataModelFixture.relationships.length)
    for (const rel of dataModelFixture.relationships) {
      const edge = graph.edges.find((e) => e.id === rel.id)
      expect(edge).toBeDefined()
      expect(edge?.source).toBe(rel.from)
      expect(edge?.target).toBe(rel.to)
      expect(edge?.label).toBe(rel.cardinality)
    }
  })

  it('carries fields including the primary-key marker', () => {
    const user = entities.find((n) => n.id === 'user')
    const data = user?.data as { fields: { name: string; pk: boolean }[] }
    expect(data.fields.find((f) => f.name === 'id')?.pk).toBe(true)
    expect(data.fields.find((f) => f.name === 'email')?.pk).toBe(false)
  })

  it('produces identical node ids when regenerated', () => {
    const again = dataModelToGraph(dataModelFixture)
    expect(again.nodes.map((n) => n.id)).toEqual(graph.nodes.map((n) => n.id))
  })

  it('tolerates a malformed entity missing its fields array', () => {
    const doc = asDataModel({
      entities: [{ id: 'ghost', name: 'Ghost', domain: 'core' }],
      relationships: [],
    })
    const node = dataModelToGraph(doc).nodes.find((n) => n.id === 'ghost')
    expect(node?.type).toBe('entity')
    expect(node?.height).toBeGreaterThan(0)
  })
})

describe('flowToGraph', () => {
  const flow = appFlowsFixture.flows[0]
  const graph = flowToGraph(flow)

  it('uses step ids as node ids verbatim and preserves kind', () => {
    expect(graph.nodes.map((n) => n.id)).toEqual(flow.steps.map((s) => s.id))
    for (const step of flow.steps) {
      const node = graph.nodes.find((n) => n.id === step.id)
      expect((node?.data as { kind: string }).kind).toBe(step.kind)
    }
  })

  it('labels edges and gives every edge a unique id', () => {
    expect(graph.edges).toHaveLength(flow.edges.length)
    const ids = new Set(graph.edges.map((e) => e.id))
    expect(ids.size).toBe(flow.edges.length)
    const labelled = graph.edges.find((e) => e.source === 'view-cart')
    expect(labelled?.label).toBe('checkout')
  })

  it('drops empty edge labels', () => {
    const blank = graph.edges.find((e) => e.source === 'submit-order')
    expect(blank?.label).toBeUndefined()
  })

  it('keeps parallel edges distinct even with the same endpoints', () => {
    const branching = flowToGraph({
      id: 'branchy',
      name: 'Branchy',
      summary: '',
      steps: [
        { id: 'a', name: 'A', kind: 'service' },
        { id: 'b', name: 'B', kind: 'service' },
      ],
      edges: [
        { from: 'a', to: 'b', label: 'retry' },
        { from: 'a', to: 'b', label: 'ok' },
      ],
    })
    expect(new Set(branching.edges.map((e) => e.id)).size).toBe(2)
  })
})
