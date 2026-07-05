import { Link } from '@tanstack/react-router'
import {
  ChartColumn,
  DollarSign,
  FileText,
  LayoutDashboard,
  Lightbulb,
  ListChecks,
  Settings,
  SquareTerminal,
  type LucideIcon,
} from 'lucide-react'

import { useAttentionCount } from '@/lib/attention'

interface NavItem {
  label: string
  icon: LucideIcon
  to: string
  exact?: boolean
  attention?: boolean
}

interface NavGroup {
  label: string
  items: NavItem[]
}

const GROUPS: NavGroup[] = [
  {
    label: 'OPERATE',
    items: [
      {
        label: 'Overview',
        icon: LayoutDashboard,
        to: '/',
        exact: true,
        attention: true,
      },
    ],
  },
  {
    label: 'OBSERVE',
    items: [
      { label: 'Runs', icon: ListChecks, to: '/runs' },
      { label: 'Terminal', icon: SquareTerminal, to: '/terminal' },
      { label: 'Costs', icon: DollarSign, to: '/costs' },
      { label: 'Analytics', icon: ChartColumn, to: '/analytics' },
      { label: 'Lessons', icon: Lightbulb, to: '/lessons' },
    ],
  },
  {
    label: 'AUTHOR',
    items: [{ label: 'PRD', icon: FileText, to: '/prd' }],
  },
  {
    label: 'CONFIGURE',
    items: [{ label: 'Settings', icon: Settings, to: '/settings' }],
  },
]

export function Sidebar() {
  const attention = useAttentionCount()
  const host = window.location.host

  return (
    <aside className="fixed inset-y-0 left-0 z-10 flex w-60 flex-col border-r border-border bg-card">
      <div className="flex h-14 items-center px-5">
        <Link
          to="/"
          className="font-mono text-lg font-medium tracking-tight text-foreground"
        >
          trau
          <span className="cursor-block text-primary">▍</span>
        </Link>
      </div>

      <nav className="flex-1 overflow-y-auto px-3 py-2">
        {GROUPS.map((group) => (
          <div key={group.label} className="mb-5">
            <p className="px-2 pb-1.5 font-mono text-[0.65rem] uppercase tracking-[0.2em] text-muted-foreground">
              {group.label}
            </p>
            <ul className="flex flex-col gap-0.5">
              {group.items.map((item) => {
                const badge =
                  item.attention && attention > 0 ? attention : null
                return (
                  <li key={item.to}>
                    <Link
                      to={item.to}
                      activeOptions={{ exact: item.exact ?? false }}
                      className="group relative flex items-center gap-2.5 rounded-md px-2 py-1.5 font-mono text-sm text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
                      activeProps={{
                        className:
                          'bg-primary/10 text-primary hover:bg-primary/10 hover:text-primary',
                      }}
                    >
                      {({ isActive }) => (
                        <>
                          {isActive && (
                            <span
                              aria-hidden="true"
                              className="absolute inset-y-1.5 left-0 w-0.5 rounded-full bg-primary"
                            />
                          )}
                          <item.icon className="size-4" aria-hidden="true" />
                          <span className="flex-1">{item.label}</span>
                          {badge ? (
                            <span className="inline-flex h-4 min-w-4 items-center justify-center rounded-full border border-warn/50 bg-warn/12 px-1 font-mono text-[0.65rem] text-warn">
                              {badge}
                            </span>
                          ) : null}
                        </>
                      )}
                    </Link>
                  </li>
                )
              })}
            </ul>
          </div>
        ))}
      </nav>

      <div className="border-t border-border px-5 py-3">
        <span className="inline-flex items-center gap-2 font-mono text-xs text-teal">
          <span aria-hidden="true">●</span>
          {host ? `serving ${host}` : 'serving'}
        </span>
      </div>
    </aside>
  )
}
