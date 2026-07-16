import {
  AppWindow,
  Box,
  Clock,
  Database,
  ExternalLink,
  Globe,
  ListOrdered,
  Server,
  type LucideIcon,
} from 'lucide-react'

import type { StepKind } from '@/lib/atlas'

export interface StepKindStyle {
  label: string
  icon: LucideIcon
  text: string
  border: string
  bg: string
  swatch: string
}

export const STEP_KINDS: Record<StepKind, StepKindStyle> = {
  ui: {
    label: 'UI',
    icon: AppWindow,
    text: 'text-brand',
    border: 'border-brand/40',
    bg: 'bg-brand/10',
    swatch: 'var(--color-brand)',
  },
  http: {
    label: 'HTTP',
    icon: Globe,
    text: 'text-info',
    border: 'border-info/40',
    bg: 'bg-info/10',
    swatch: 'var(--color-info)',
  },
  service: {
    label: 'Service',
    icon: Server,
    text: 'text-teal',
    border: 'border-teal/40',
    bg: 'bg-teal/10',
    swatch: 'var(--color-teal)',
  },
  job: {
    label: 'Job',
    icon: Clock,
    text: 'text-warn',
    border: 'border-warn/40',
    bg: 'bg-warn/10',
    swatch: 'var(--color-warn)',
  },
  queue: {
    label: 'Queue',
    icon: ListOrdered,
    text: 'text-cli',
    border: 'border-cli/40',
    bg: 'bg-cli/10',
    swatch: 'var(--color-cli)',
  },
  db: {
    label: 'DB',
    icon: Database,
    text: 'text-done',
    border: 'border-done/40',
    bg: 'bg-done/10',
    swatch: 'var(--color-done)',
  },
  external: {
    label: 'External',
    icon: ExternalLink,
    text: 'text-muted-foreground',
    border: 'border-border',
    bg: 'bg-muted/40',
    swatch: 'var(--color-muted-foreground)',
  },
  other: {
    label: 'Other',
    icon: Box,
    text: 'text-muted-foreground',
    border: 'border-border',
    bg: 'bg-secondary/60',
    swatch: 'var(--color-faint)',
  },
}

export function stepKindStyle(kind: StepKind): StepKindStyle {
  return STEP_KINDS[kind] ?? STEP_KINDS.other
}
