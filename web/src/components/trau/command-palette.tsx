import { useEffect } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { Check, FolderGit2, GitBranch } from 'lucide-react'

import { ALL_SCOPE, useActiveRepo } from '@/components/trau/active-repo'
import { NAV_GROUPS, type NavItem } from '@/components/trau/nav-items'
import {
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from '@/components/ui/command'
import { isPaletteShortcut } from '@/lib/palette-keys'

const GROUP_HEADING =
  '[&_[cmdk-group-heading]]:font-mono [&_[cmdk-group-heading]]:text-[0.65rem] [&_[cmdk-group-heading]]:uppercase [&_[cmdk-group-heading]]:tracking-[0.2em] [&_[cmdk-group-heading]]:font-normal'

export function CommandPalette({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { repo, repos, isAll, setScope, autoScope, openSwitcher } =
    useActiveRepo()
  const navigate = useNavigate()

  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if (!isPaletteShortcut(e)) return
      e.preventDefault()
      onOpenChange(!open)
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [open, onOpenChange])

  function pickScope(scope: string) {
    setScope(scope)
    onOpenChange(false)
  }

  // A gated destination under "All repos" mirrors the sidebar: auto-scope to a
  // sensible repo and follow the link, or hand off to the pulsing switcher.
  function goTo(item: NavItem) {
    onOpenChange(false)
    if (isAll && item.requiresProject && !autoScope()) {
      openSwitcher()
      return
    }
    void navigate({ to: item.to, search: item.search })
  }

  return (
    <CommandDialog open={open} onOpenChange={onOpenChange} className="font-mono">
      <CommandInput placeholder="Type a command or search…" />
      <CommandList>
        <CommandEmpty>No results.</CommandEmpty>
        {repos.length > 0 && (
          <>
            <CommandGroup heading="Projects" className={GROUP_HEADING}>
              {repos.length > 1 && (
                <CommandItem
                  value="All repos"
                  onSelect={() => pickScope(ALL_SCOPE)}
                >
                  <FolderGit2 />
                  <span className="flex-1 truncate">All repos</span>
                  {isAll && <Check className="text-primary" />}
                </CommandItem>
              )}
              {repos.map((r) => (
                <CommandItem
                  key={r.name}
                  value={r.name}
                  keywords={[r.root]}
                  onSelect={() => pickScope(r.name)}
                >
                  <GitBranch />
                  <span className="flex min-w-0 flex-1 flex-col">
                    <span className="truncate">{r.name}</span>
                    <span className="truncate text-[0.65rem] text-muted-foreground">
                      {r.root}
                    </span>
                  </span>
                  {!isAll && r.name === repo && (
                    <Check className="text-primary" />
                  )}
                </CommandItem>
              ))}
            </CommandGroup>
            <CommandSeparator />
          </>
        )}
        <CommandGroup heading="Navigation" className={GROUP_HEADING}>
          {NAV_GROUPS.flatMap((group) => group.items).map((item) => (
            <CommandItem
              key={item.to}
              value={item.label}
              keywords={[item.to]}
              onSelect={() => goTo(item)}
            >
              <item.icon />
              <span className="flex-1 truncate">{item.label}</span>
            </CommandItem>
          ))}
        </CommandGroup>
      </CommandList>
    </CommandDialog>
  )
}
