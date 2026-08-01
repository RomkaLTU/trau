import {
  Download,
  Moon,
  Play,
  Rocket,
  RotateCw,
  Square,
  Sun,
  type LucideIcon,
} from 'lucide-react'

import { externalTracker } from './instances'
import { matchesQuery } from './palette-filter'
import type { ResolvedTheme } from './theme'
import { versionLabel, type UpdateStatus } from './update'

export type PaletteActionId =
  | 'start-loop'
  | 'stop-loop'
  | 'sync-backlog'
  | 'run-next'
  | 'toggle-theme'
  | 'check-updates'

export interface PaletteAction {
  id: PaletteActionId
  label: string
  icon: LucideIcon
  requiresProject?: boolean
  keywords?: string[]
}

export interface ActionContext {
  // The repo the repo-scoped rows describe, empty under "All projects" with no
  // auto-scope target: there is no state to read, so the rows stand for the repo
  // handoff that opens the switcher.
  repo: string
  // Both stay undefined until that repo's queue and health land. A row that would
  // name the wrong state — Start for a repo already draining, a sync a tracker-less
  // repo would refuse — waits instead of showing it.
  draining?: boolean
  provider?: string
  theme: ResolvedTheme
}

export function paletteActions({
  repo,
  draining,
  provider,
  theme,
}: ActionContext): PaletteAction[] {
  const unscoped = repo === ''
  const dark = theme === 'dark'
  const actions: PaletteAction[] = []
  if (unscoped || draining !== undefined) {
    actions.push(
      draining
        ? {
            id: 'stop-loop',
            label: 'Stop loop',
            icon: Square,
            requiresProject: true,
          }
        : {
            id: 'start-loop',
            label: 'Start loop',
            icon: Play,
            requiresProject: true,
          },
    )
  }
  if (unscoped || externalTracker(provider)) {
    actions.push({
      id: 'sync-backlog',
      label: 'Sync backlog',
      icon: RotateCw,
      requiresProject: true,
      keywords: ['tracker'],
    })
  }
  actions.push(
    {
      id: 'run-next',
      label: 'Run next',
      icon: Rocket,
      requiresProject: true,
      keywords: ['run once'],
    },
    {
      id: 'toggle-theme',
      label: dark ? 'Switch to light mode' : 'Switch to dark mode',
      icon: dark ? Sun : Moon,
      keywords: ['theme'],
    },
    { id: 'check-updates', label: 'Check for updates', icon: Download },
  )
  return actions
}

export function matchActions(
  actions: readonly PaletteAction[],
  query: string,
): PaletteAction[] {
  return actions.filter((action) =>
    matchesQuery(query, [action.label, ...(action.keywords ?? [])]),
  )
}

export function updateCheckMessage(status: UpdateStatus): string {
  if (status.updateAvailable) {
    return `Update available — ${versionLabel(status.latest)}`
  }
  return `Up to date — ${versionLabel(status.running)}`
}
