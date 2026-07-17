import { useCallback, useEffect, useState } from 'react'
import { Monitor, Moon, Sun, type LucideIcon } from 'lucide-react'

import {
  applyTheme,
  loadThemeMode,
  storeThemeMode,
  type ResolvedTheme,
  type ThemeMode,
} from '@/lib/theme'
import { cn } from '@/lib/utils'

export function useTheme(): {
  mode: ThemeMode
  setMode: (mode: ThemeMode) => void
} {
  const [mode, setModeState] = useState<ThemeMode>(loadThemeMode)

  useEffect(() => {
    applyTheme(mode)
    if (mode !== 'system') return
    const media = window.matchMedia('(prefers-color-scheme: dark)')
    const onChange = () => applyTheme('system')
    media.addEventListener('change', onChange)
    return () => media.removeEventListener('change', onChange)
  }, [mode])

  const setMode = useCallback((next: ThemeMode) => {
    storeThemeMode(next)
    setModeState(next)
  }, [])

  return { mode, setMode }
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

  return (
    <div
      role="radiogroup"
      aria-label="Color theme"
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
