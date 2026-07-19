// @vitest-environment happy-dom
import { act, createElement } from 'react'
import { createRoot } from 'react-dom/client'
import { describe, expect, it } from 'vitest'

import { Markdown, parseBlocks } from './markdown'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true

describe('parseBlocks', () => {
  it('parses a GFM table into header and rows', () => {
    const md = [
      '| col a | col b |',
      '| ----- | ----- |',
      '| val 1 | val 2 |',
      '| val 3 |       |',
    ].join('\n')
    expect(parseBlocks(md)).toEqual([
      {
        kind: 'table',
        header: ['col a', 'col b'],
        rows: [
          ['val 1', 'val 2'],
          ['val 3', ''],
        ],
      },
    ])
  })

  it('ends the preceding paragraph where a table starts', () => {
    const md = 'intro\n| a | b |\n| --- | --- |\n| 1 | 2 |'
    expect(parseBlocks(md)).toEqual([
      { kind: 'paragraph', text: 'intro' },
      { kind: 'table', header: ['a', 'b'], rows: [['1', '2']] },
    ])
  })

  it('treats a pipe row without a delimiter line as a paragraph', () => {
    expect(parseBlocks('before\n| just | text |')).toEqual([
      { kind: 'paragraph', text: 'before | just | text |' },
    ])
  })
})

describe('Markdown', () => {
  it('renders a GFM table as an HTML table, not literal text', async () => {
    const md = '| col a | col b |\n| ----- | ----- |\n| val 1 | val 2 |'
    const container = document.createElement('div')
    const root = createRoot(container)
    await act(async () => {
      root.render(createElement(Markdown, { children: md }))
    })
    expect(container.querySelector('table')).not.toBeNull()
    expect(container.querySelectorAll('th')).toHaveLength(2)
    expect(container.querySelectorAll('td')).toHaveLength(2)
    expect(container.textContent).not.toContain('|')
    root.unmount()
  })
})
