import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { useNavigate } from '@tanstack/react-router'

import { useActiveRepo } from '@/components/trau/active-repo'
import { CommandPalette } from '@/components/trau/command-palette'
import {
  G_TARGET_KEYS,
  gTarget,
  type NavItem,
} from '@/components/trau/nav-items'
import { RecentsTracker } from '@/components/trau/recents-tracker'
import { RepoSwitcherDialog } from '@/components/trau/repo-switcher'
import { ShortcutsDialog } from '@/components/trau/shortcuts-dialog'
import { Sidebar } from '@/components/trau/sidebar'
import { readKeyStroke } from '@/lib/keys'
import { gSequenceStep, opensShortcutsHelp } from '@/lib/shortcut-keys'

// One dialog at a time: opening any of them closes whichever was up, so ⌘K,
// ⌘P and ? can never stack two modals on each other.
type OpenDialog = 'palette' | 'switcher' | 'shortcuts' | null

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
  const setShortcuts = useCallback(
    (open: boolean) => setDialog(open ? 'shortcuts' : null),
    [],
  )

  const navigate = useNavigate()
  const { isAll, autoScope, openSwitcher } = useActiveRepo()

  // A gated jump takes the sidebar's own recovery: auto-scope to a lone/last-used
  // repo and go, or open the switcher when there is a real choice.
  const jumpTo = useCallback(
    (item: NavItem) => {
      if (isAll && item.requiresProject && !autoScope()) {
        openSwitcher()
        return
      }
      void navigate({ to: item.to, search: item.search })
    },
    [isAll, autoScope, openSwitcher, navigate],
  )

  // The g navigator and ? live on the document: they answer from every page.
  const armedAt = useRef<number | null>(null)
  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      const stroke = readKeyStroke(e)
      if (opensShortcutsHelp(stroke)) {
        e.preventDefault()
        armedAt.current = null
        setShortcuts(true)
        return
      }
      const step = gSequenceStep(
        stroke,
        armedAt.current,
        Date.now(),
        G_TARGET_KEYS,
      )
      switch (step.kind) {
        case 'arm':
          armedAt.current = Date.now()
          break
        case 'disarm':
          armedAt.current = null
          break
        case 'go': {
          armedAt.current = null
          const item = gTarget(step.key)
          if (item) jumpTo(item)
          break
        }
        case 'idle':
          break
      }
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [jumpTo, setShortcuts])

  return (
    <div className="relative min-h-screen">
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:fixed focus:top-3 focus:left-3 focus:z-50 focus:rounded-md focus:border focus:border-border focus:bg-popover focus:px-3 focus:py-2 focus:font-mono focus:text-sm focus:text-foreground focus:shadow-lg"
      >
        Skip to content
      </a>
      <Sidebar
        onOpenPalette={() => setPalette(true)}
        onOpenSwitcher={() => setSwitcher(true)}
      />
      <main
        id="main"
        tabIndex={-1}
        className="relative z-[1] ml-60 min-h-screen"
      >
        {children}
      </main>
      <CommandPalette open={dialog === 'palette'} onOpenChange={setPalette} />
      <RepoSwitcherDialog
        open={dialog === 'switcher'}
        onOpenChange={setSwitcher}
      />
      <ShortcutsDialog
        open={dialog === 'shortcuts'}
        onOpenChange={setShortcuts}
      />
      <RecentsTracker />
    </div>
  )
}
