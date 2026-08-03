import { useCallback, useState, type ReactNode } from 'react'

import { CommandPalette } from '@/components/trau/command-palette'
import { RecentsTracker } from '@/components/trau/recents-tracker'
import { RepoSwitcherDialog } from '@/components/trau/repo-switcher'
import { Sidebar } from '@/components/trau/sidebar'

// One dialog at a time: opening either of them closes whichever was up, so ⌘K
// and ⌘P can never stack two modals on each other.
type OpenDialog = 'palette' | 'switcher' | null

export function AppShell({ children }: { children: ReactNode }) {
  const [dialog, setDialog] = useState<OpenDialog>(null)
  const setPalette = useCallback(
    (open: boolean) => setDialog(open ? 'palette' : null),
    [],
  )
  const setSwitcher = useCallback(
    (open: boolean) => setDialog(open ? 'switcher' : null),
    [],
  )

  return (
    <div className="relative min-h-screen">
      <Sidebar
        onOpenPalette={() => setPalette(true)}
        onOpenSwitcher={() => setSwitcher(true)}
      />
      <main className="relative z-[1] ml-60 min-h-screen">{children}</main>
      <CommandPalette open={dialog === 'palette'} onOpenChange={setPalette} />
      <RepoSwitcherDialog
        open={dialog === 'switcher'}
        onOpenChange={setSwitcher}
      />
      <RecentsTracker />
    </div>
  )
}
