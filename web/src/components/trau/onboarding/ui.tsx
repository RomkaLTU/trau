import type { InputHTMLAttributes, ReactNode } from 'react'
import { Check, Info, PenLine, TriangleAlert, X, type LucideIcon } from 'lucide-react'

import { secretPlaceholder, type CredentialLayer } from '@/lib/onboarding'
import { cn } from '@/lib/utils'

export function FieldLabel({
  htmlFor,
  children,
  className,
}: {
  htmlFor?: string
  children: ReactNode
  className?: string
}) {
  const base =
    'font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground'
  if (htmlFor) {
    return (
      <label htmlFor={htmlFor} className={cn(base, className)}>
        {children}
      </label>
    )
  }
  return <span className={cn(base, className)}>{children}</span>
}

export function Hint({
  children,
  className,
}: {
  children: ReactNode
  className?: string
}) {
  return (
    <p className={cn('font-sans text-xs leading-relaxed text-muted-foreground', className)}>
      {children}
    </p>
  )
}

export function TextInput({
  invalid,
  className,
  ...props
}: InputHTMLAttributes<HTMLInputElement> & { invalid?: boolean }) {
  return (
    <input
      autoComplete="off"
      spellCheck={false}
      aria-invalid={invalid || undefined}
      className={cn(
        'w-full rounded-md border bg-input px-2.5 py-1.5 font-mono text-sm text-foreground outline-none transition-colors placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring/50',
        invalid ? 'border-fail/60 focus-visible:border-fail' : 'border-border',
        className,
      )}
      {...props}
    />
  )
}

export function SecretInput({
  id,
  label,
  placeholder,
  hasExisting,
  existingLayer,
  value,
  onChange,
}: {
  id: string
  label: string
  placeholder: string
  hasExisting?: boolean
  existingLayer?: CredentialLayer
  value: string
  onChange: (value: string) => void
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex items-center gap-2">
        <FieldLabel htmlFor={id}>{label}</FieldLabel>
        <span
          className="inline-flex items-center gap-1 rounded border border-border bg-muted/60 px-1.5 py-0.5 font-mono text-[0.6rem] text-muted-foreground"
          title="Secrets are write-only in the web UI — they can be replaced but never read back."
        >
          <PenLine className="size-2.5" aria-hidden="true" />
          write-only
        </span>
      </div>
      <input
        id={id}
        type="password"
        autoComplete="new-password"
        spellCheck={false}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={secretPlaceholder(Boolean(hasExisting), placeholder)}
        className="w-full rounded-md border border-border bg-input px-2.5 py-1.5 font-mono text-sm text-foreground outline-none transition-colors placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
      />
      {hasExisting && (
        <Hint>
          A key is already stored in the{' '}
          {existingLayer === 'user' ? 'user config (~/.trau.ini)' : 'project config'}.
          Leave blank to keep it.
        </Hint>
      )}
    </div>
  )
}

export type CalloutTone = 'fail' | 'warn' | 'success' | 'info'

const CALLOUT_STYLES: Record<
  CalloutTone,
  { box: string; icon: string; Icon: LucideIcon }
> = {
  fail: { box: 'border-fail/40 bg-fail/5', icon: 'text-fail', Icon: X },
  warn: { box: 'border-warn/40 bg-warn/5', icon: 'text-warn', Icon: TriangleAlert },
  success: { box: 'border-done/40 bg-done/5', icon: 'text-done', Icon: Check },
  info: { box: 'border-info/40 bg-info/5', icon: 'text-info', Icon: Info },
}

export function Callout({
  tone,
  title,
  children,
  actions,
  className,
}: {
  tone: CalloutTone
  title: string
  children?: ReactNode
  actions?: ReactNode
  className?: string
}) {
  const { box, icon, Icon } = CALLOUT_STYLES[tone]
  return (
    <div
      role={tone === 'fail' ? 'alert' : 'status'}
      className={cn('flex items-start gap-2.5 rounded-md border px-3 py-3', box, className)}
    >
      <Icon className={cn('mt-0.5 size-3.5 shrink-0', icon)} aria-hidden="true" />
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        <p className="font-mono text-sm text-foreground">{title}</p>
        {children && (
          <div className="font-sans text-xs leading-relaxed text-muted-foreground">
            {children}
          </div>
        )}
        {actions && (
          <div className="mt-1.5 flex flex-wrap items-center gap-2">{actions}</div>
        )}
      </div>
    </div>
  )
}

export function Toggle({
  id,
  checked,
  onChange,
  label,
  description,
}: {
  id: string
  checked: boolean
  onChange: (checked: boolean) => void
  label: string
  description?: ReactNode
}) {
  return (
    <div className="flex items-start gap-3">
      <button
        type="button"
        role="switch"
        id={id}
        aria-checked={checked}
        onClick={() => onChange(!checked)}
        className={cn(
          'relative mt-0.5 h-5 w-9 shrink-0 rounded-full border transition-colors outline-none focus-visible:ring-2 focus-visible:ring-ring/50',
          checked ? 'border-primary/60 bg-primary/30' : 'border-border bg-input',
        )}
      >
        <span
          aria-hidden="true"
          className={cn(
            'absolute top-1/2 size-3.5 -translate-y-1/2 rounded-full transition-all',
            checked ? 'left-[calc(100%-1.125rem)] bg-primary' : 'left-0.5 bg-muted-foreground',
          )}
        />
      </button>
      <div className="flex flex-col gap-0.5">
        <label htmlFor={id} className="cursor-pointer font-mono text-sm text-foreground">
          {label}
        </label>
        {description && <Hint>{description}</Hint>}
      </div>
    </div>
  )
}
