import { useEffect, useState } from 'react'
import { Monitor, Moon, Sun, type LucideIcon } from 'lucide-react'

import {
  applyTheme,
  storeThemeMode,
  useThemeMode,
  type ResolvedTheme,
  type ThemeMode,
} from '@/lib/theme'
import { useRovingGroup } from '@/lib/use-roving-group'
import { cn } from '@/lib/utils'

export function useTheme(): {
  mode: ThemeMode
  setMode: (mode: ThemeMode) => void
} {
  const mode = useThemeMode()

  useEffect(() => {
    applyTheme(mode)
    if (mode !== 'system') return
    const media = window.matchMedia('(prefers-color-scheme: dark)')
    const onChange = () => applyTheme('system')
    media.addEventListener('change', onChange)
    return () => media.removeEventListener('change', onChange)
  }, [mode])

  return { mode, setMode: storeThemeMode }
}

export function useResolvedTheme(): ResolvedTheme {
  const [theme, setTheme] = useState<ResolvedTheme>(readResolvedTheme)

  useEffect(() => {
    const root = document.documentElement
    const update = () => setTheme(readResolvedTheme())
    update()
    const observer = new MutationObserver(update)
    observer.observe(root, { attributes: true, attributeFilter: ['class'] })
    return () => observer.disconnect()
  }, [])

  return theme
}

function readResolvedTheme(): ResolvedTheme {
  return globalThis.document?.documentElement.classList.contains('dark')
    ? 'dark'
    : 'light'
}

const OPTIONS: readonly { value: ThemeMode; label: string; icon: LucideIcon }[] =
  [
    { value: 'system', label: 'System', icon: Monitor },
    { value: 'light', label: 'Light', icon: Sun },
    { value: 'dark', label: 'Dark', icon: Moon },
  ]

export function ThemeToggle({ className }: { className?: string }) {
  const { mode, setMode } = useTheme()
  const roving = useRovingGroup({
    keys: OPTIONS.map((option) => option.value),
    selected: mode,
    onSelect: (key) => setMode(key as ThemeMode),
  })

  return (
    <div
      role="radiogroup"
      aria-label="Color theme"
      {...roving.groupProps}
      className={cn(
        'inline-flex w-fit rounded-md border border-border bg-input p-0.5',
        className,
      )}
    >
      {OPTIONS.map((option) => {
        const active = option.value === mode
        return (
          <button
            key={option.value}
            type="button"
            role="radio"
            aria-checked={active}
            aria-label={option.label}
            title={option.label}
            {...roving.itemProps(option.value)}
            onClick={() => setMode(option.value)}
            className={cn(
              'rounded-[calc(var(--radius)-6px)] px-2.5 py-1 transition-colors',
              active
                ? 'bg-primary text-primary-foreground'
                : 'text-muted-foreground hover:text-foreground',
            )}
          >
            <option.icon className="size-3.5" aria-hidden="true" />
          </button>
        )
      })}
    </div>
  )
}
