import type { ReactNode } from 'react'

import { Sidebar } from '@/components/trau/sidebar'

export function AppShell({ children }: { children: ReactNode }) {
  return (
    <div className="relative min-h-screen">
      <Sidebar />
      <main className="relative z-[1] ml-60 min-h-screen">{children}</main>
    </div>
  )
}
