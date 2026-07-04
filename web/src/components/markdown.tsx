import { type ReactNode } from 'react'

import { cn } from '@/lib/utils'

type Block =
  | { kind: 'heading'; level: number; text: string }
  | { kind: 'code'; text: string }
  | { kind: 'list'; ordered: boolean; items: string[] }
  | { kind: 'paragraph'; text: string }

const HEADING = /^(#{1,6})\s+(.*)$/
const BULLET = /^\s*[-*]\s+/
const ORDERED = /^\s*\d+\.\s+/

function isBlockStart(line: string): boolean {
  return (
    HEADING.test(line) ||
    BULLET.test(line) ||
    ORDERED.test(line) ||
    line.trimStart().startsWith('```')
  )
}

function parseBlocks(md: string): Block[] {
  const lines = md.replace(/\r\n/g, '\n').split('\n')
  const blocks: Block[] = []
  let i = 0

  while (i < lines.length) {
    const line = lines[i]

    if (line.trim() === '') {
      i++
      continue
    }

    if (line.trimStart().startsWith('```')) {
      const body: string[] = []
      i++
      while (i < lines.length && !lines[i].trimStart().startsWith('```')) {
        body.push(lines[i])
        i++
      }
      i++
      blocks.push({ kind: 'code', text: body.join('\n') })
      continue
    }

    const heading = HEADING.exec(line)
    if (heading) {
      blocks.push({ kind: 'heading', level: heading[1].length, text: heading[2].trim() })
      i++
      continue
    }

    if (BULLET.test(line) || ORDERED.test(line)) {
      const ordered = ORDERED.test(line)
      const marker = ordered ? ORDERED : BULLET
      const items: string[] = []
      while (i < lines.length && marker.test(lines[i])) {
        items.push(lines[i].replace(marker, ''))
        i++
      }
      blocks.push({ kind: 'list', ordered, items })
      continue
    }

    const para: string[] = []
    while (i < lines.length && lines[i].trim() !== '' && !isBlockStart(lines[i])) {
      para.push(lines[i].trim())
      i++
    }
    blocks.push({ kind: 'paragraph', text: para.join(' ') })
  }

  return blocks
}

const INLINE = /`([^`]+)`|\*\*([^*]+)\*\*/g

function renderInline(text: string): ReactNode[] {
  const nodes: ReactNode[] = []
  let last = 0
  let key = 0
  for (let m = INLINE.exec(text); m !== null; m = INLINE.exec(text)) {
    if (m.index > last) {
      nodes.push(text.slice(last, m.index))
    }
    if (m[1] !== undefined) {
      nodes.push(
        <code key={key++} className="rounded bg-muted px-1 py-0.5 font-mono text-[0.85em]">
          {m[1]}
        </code>,
      )
    } else {
      nodes.push(
        <strong key={key++} className="font-semibold text-foreground">
          {m[2]}
        </strong>,
      )
    }
    last = m.index + m[0].length
  }
  if (last < text.length) {
    nodes.push(text.slice(last))
  }
  return nodes
}

const headingClass: Record<number, string> = {
  1: 'text-lg font-semibold',
  2: 'text-base font-semibold',
  3: 'text-sm font-semibold',
}

function Block({ block }: { block: Block }) {
  switch (block.kind) {
    case 'heading':
      return (
        <p className={cn('mt-4 first:mt-0 text-foreground', headingClass[block.level] ?? headingClass[3])}>
          {renderInline(block.text)}
        </p>
      )
    case 'code':
      return (
        <pre className="mt-3 overflow-x-auto rounded-md bg-muted p-3 font-mono text-xs">
          <code>{block.text}</code>
        </pre>
      )
    case 'list': {
      const Tag = block.ordered ? 'ol' : 'ul'
      return (
        <Tag
          className={cn(
            'mt-2 flex flex-col gap-1 pl-5',
            block.ordered ? 'list-decimal' : 'list-disc',
          )}
        >
          {block.items.map((item, i) => (
            <li key={i} className="pl-1">
              {renderInline(item)}
            </li>
          ))}
        </Tag>
      )
    }
    case 'paragraph':
      return <p className="mt-2 first:mt-0 leading-relaxed">{renderInline(block.text)}</p>
  }
}

export function Markdown({ children, className }: { children: string; className?: string }) {
  const blocks = parseBlocks(children)
  return (
    <div className={cn('text-sm text-muted-foreground', className)}>
      {blocks.map((block, i) => (
        <Block key={i} block={block} />
      ))}
    </div>
  )
}
