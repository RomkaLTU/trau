import type { ReactNode } from 'react'

import { cn } from '@/lib/utils'

export interface Column<T> {
  key: string
  header: ReactNode
  align?: 'left' | 'right'
  className?: string
  render: (row: T) => ReactNode
}

export function DataTable<T>({
  columns,
  rows,
  getRowKey,
  className,
}: {
  columns: readonly Column<T>[]
  rows: readonly T[]
  getRowKey: (row: T) => string
  className?: string
}) {
  return (
    <div className={cn('overflow-x-auto', className)}>
      <table className="w-full border-collapse font-mono text-xs">
        <thead>
          <tr className="border-b border-border text-left text-muted-foreground">
            {columns.map((col) => (
              <th
                key={col.key}
                className={cn(
                  'px-4 py-2 font-normal tracking-wider',
                  col.align === 'right' && 'text-right',
                )}
              >
                {col.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr
              key={getRowKey(row)}
              className="border-b border-border/60 last:border-0 hover:bg-secondary/40"
            >
              {columns.map((col) => (
                <td
                  key={col.key}
                  className={cn(
                    'px-4 py-2.5 text-foreground',
                    col.align === 'right' && 'text-right',
                    col.className,
                  )}
                >
                  {col.render(row)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
