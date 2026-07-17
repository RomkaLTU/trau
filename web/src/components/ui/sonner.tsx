import type { CSSProperties } from 'react'
import { Toaster as Sonner, type ToasterProps } from 'sonner'

import { useResolvedTheme } from '@/components/trau/theme-toggle'

export function Toaster(props: ToasterProps) {
  const theme = useResolvedTheme()

  return (
    <Sonner
      theme={theme}
      position="bottom-right"
      className="toaster group"
      style={
        {
          '--normal-bg': 'var(--popover)',
          '--normal-text': 'var(--popover-foreground)',
          '--normal-border': 'var(--border)',
        } as CSSProperties
      }
      {...props}
    />
  )
}
