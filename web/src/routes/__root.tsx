import type { QueryClient } from '@tanstack/react-query'
import {
  Link,
  Outlet,
  createRootRouteWithContext,
} from '@tanstack/react-router'
import {
  Boxes,
  DollarSign,
  LayoutDashboard,
  ListChecks,
  Settings,
  type LucideIcon,
} from 'lucide-react'

interface NavItem {
  to: string
  label: string
  icon: LucideIcon
  exact?: boolean
}

const nav: NavItem[] = [
  { to: '/', label: 'Overview', icon: LayoutDashboard, exact: true },
  { to: '/instances', label: 'Instances', icon: Boxes },
  { to: '/runs', label: 'Runs', icon: ListChecks },
  { to: '/costs', label: 'Costs', icon: DollarSign },
  { to: '/settings', label: 'Settings', icon: Settings },
]

export const Route = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  component: RootLayout,
})

function RootLayout() {
  return (
    <div className="min-h-svh">
      <header className="border-b">
        <div className="mx-auto flex h-14 max-w-5xl items-center gap-6 px-6">
          <Link to="/" className="font-semibold tracking-wide">
            trau
          </Link>
          <nav className="flex items-center gap-1">
            {nav.map(({ to, label, icon: Icon, exact }) => (
              <Link
                key={to}
                to={to}
                activeOptions={{ exact }}
                className="flex items-center gap-2 rounded-md px-3 py-1.5 text-sm text-muted-foreground transition-colors hover:text-foreground"
                activeProps={{ className: 'bg-accent text-accent-foreground' }}
              >
                <Icon className="size-4" />
                {label}
              </Link>
            ))}
          </nav>
        </div>
      </header>
      <main className="mx-auto max-w-5xl px-6 py-8">
        <Outlet />
      </main>
    </div>
  )
}
