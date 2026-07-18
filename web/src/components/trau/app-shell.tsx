import { useState, type ReactNode } from 'react'

import { CommandPalette } from '@/components/trau/command-palette'
import { RecentsTracker } from '@/components/trau/recents-tracker'
import { Sidebar } from '@/components/trau/sidebar'

export function AppShell({ children }: { children: ReactNode }) {
  const [paletteOpen, setPaletteOpen] = useState(false)

  return (
    <div className="relative min-h-screen">
      <Sidebar onOpenPalette={() => setPaletteOpen(true)} />
      <main className="relative z-[1] ml-60 min-h-screen">{children}</main>
      <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} />
      <RecentsTracker />
    </div>
  )
}
