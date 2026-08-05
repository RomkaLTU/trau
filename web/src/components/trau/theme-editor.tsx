import { useEffect, useMemo, useRef, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Check, Copy, Download, Plus, TriangleAlert, X } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { ConfirmDialog } from '@/components/trau/confirm-dialog'
import { useResolvedTheme } from '@/components/trau/theme-toggle'
import { cn } from '@/lib/utils'
import { copyText } from '@/lib/clipboard'
import { resolveDraft, saveTheme, type ThemeSummary } from '@/lib/palette'
import { previewPalettes, type ResolvedTheme } from '@/lib/theme'
import {
  CORE_ROLES,
  EDITOR_MODES,
  addMode,
  downloadName,
  draftIssues,
  draftModes,
  exportDocument,
  hasIssues,
  isThemeColor,
  pinCounts,
  previewDocument,
  removeMode,
  seedRoles,
  setRole,
  slugify,
  themeJSON,
  type ThemeDraft,
} from '@/lib/theme-editor'

// The debounce the live preview runs on. Long enough that typing a six-digit hex
// is one resolve rather than six, short enough that the app repaints while the
// user is still looking at the swatch they changed.
const PREVIEW_MS = 200

export function ThemeEditor({
  draft: initial,
  themes,
  onCancel,
  onSaved,
}: {
  draft: ThemeDraft
  themes: ThemeSummary[]
  onCancel: () => void
  onSaved: (slug: string, name: string, replaced: boolean) => void
}) {
  const queryClient = useQueryClient()
  const viewing = useResolvedTheme()
  const [draft, setDraft] = useState(initial)
  const [mode, setMode] = useState<ResolvedTheme>(
    () => (draft.modes[viewing] ? viewing : draftModes(draft)[0]) ?? 'light',
  )
  const [confirmOverwrite, setConfirmOverwrite] = useState(false)
  const [dropPins, setDropPins] = useState(false)
  const [copied, setCopied] = useState(false)
  const [previewError, setPreviewError] = useState<string | null>(null)
  const download = useRef<HTMLAnchorElement>(null)
  const pins = pinCounts(draft)

  const reserved = useMemo(
    () => themes.filter((t) => t.origin !== 'local').map((t) => t.slug),
    [themes],
  )
  const saved = useMemo(
    () => themes.filter((t) => t.origin === 'local').map((t) => t.slug),
    [themes],
  )
  const slug = slugify(draft.name)
  const issues = draftIssues(draft, reserved)
  const blocked = hasIssues(issues)
  const modes = draftModes(draft)

  // The whole app previews the draft while the editor is open: the resolved
  // variables are applied in the browser only, and dropping them on unmount is
  // what makes Cancel instant. A resolve still in flight is disowned rather than
  // awaited — landing after Cancel it would re-apply the preview the user just
  // dropped, and landing after a newer edit it would paint a stale draft.
  useEffect(() => {
    const document = previewDocument(draft, dropPins)
    let live = true
    const timer = setTimeout(() => {
      resolveDraft(document)
        .then((palettes) => {
          if (!live) return
          previewPalettes(palettes)
          setPreviewError(null)
        })
        .catch((err: unknown) => {
          if (!live) return
          setPreviewError((err as Error).message)
        })
    }, PREVIEW_MS)
    return () => {
      live = false
      clearTimeout(timer)
    }
  }, [draft, dropPins])

  useEffect(() => () => previewPalettes(null), [])

  const mutation = useMutation({
    mutationFn: () => saveTheme(exportDocument(draft, dropPins)),
    onSuccess: async (result) => {
      await queryClient.invalidateQueries({ queryKey: ['themes'] })
      previewPalettes(null)
      onSaved(result.theme.slug, result.theme.name, result.replaced)
    },
  })

  const save = () => {
    if (blocked) return
    if (saved.includes(slug) && slug !== draft.source) {
      setConfirmOverwrite(true)
      return
    }
    mutation.mutate()
  }

  const copy = () => {
    void copyText(themeJSON(exportDocument(draft, dropPins))).then((ok) => {
      setCopied(ok)
      setTimeout(() => setCopied(false), 2000)
    })
  }

  const saveFile = () => {
    const doc = exportDocument(draft, dropPins)
    const url = URL.createObjectURL(
      new Blob([themeJSON(doc)], { type: 'application/json' }),
    )
    const link = download.current
    if (!link) return
    link.href = url
    link.download = downloadName(doc)
    link.click()
    URL.revokeObjectURL(url)
  }

  return (
    <div className="flex flex-col gap-4 rounded-lg border border-ring/40 bg-secondary/20 p-4">
      <div className="flex flex-wrap items-end gap-x-4 gap-y-2">
        <label className="flex min-w-0 flex-col gap-1">
          <span className="font-mono text-[0.7rem] text-muted-foreground">
            theme name
          </span>
          <Input
            value={draft.name}
            onChange={(e) => setDraft({ ...draft, name: e.target.value })}
            placeholder="My Theme"
            aria-label="Theme name"
            spellCheck={false}
            className="h-auto w-56 px-2 py-1.5 text-sm placeholder:text-faint"
          />
        </label>
        <span className="pb-1.5 font-mono text-[0.7rem] text-faint">
          saves as{' '}
          <span className="text-muted-foreground">
            {slug === '' ? '—' : `${slug}.json`}
          </span>
        </span>
        <div className="ml-auto flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={copy}
            disabled={blocked}
            className="h-8 gap-1.5 font-mono text-xs"
          >
            {copied ? (
              <Check className="size-3.5" aria-hidden="true" />
            ) : (
              <Copy className="size-3.5" aria-hidden="true" />
            )}
            {copied ? 'copied' : 'Copy JSON'}
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={saveFile}
            disabled={blocked}
            className="h-8 gap-1.5 font-mono text-xs"
          >
            <Download className="size-3.5" aria-hidden="true" />
            Download .json
          </Button>
          {/* eslint-disable-next-line jsx-a11y/anchor-has-content */}
          <a ref={download} className="hidden" aria-hidden="true" />
        </div>
      </div>

      <p className="text-xs leading-relaxed text-muted-foreground">
        The whole app previews this draft while the editor is open — nothing is
        saved until you save it. The file below is the shareable theme: the same
        document trau.sh will take when community themes open (see docs/themes.md).
      </p>

      {pins.vars + pins.tui > 0 && (
        <label className="flex flex-wrap items-start gap-2 rounded-md border border-border bg-card/60 px-3 py-2 text-xs leading-relaxed text-muted-foreground">
          <input
            type="checkbox"
            checked={dropPins}
            onChange={(e) => setDropPins(e.target.checked)}
            className="mt-0.5 size-3.5 shrink-0 accent-[var(--primary)]"
          />
          <span className="min-w-0">
            <span className="font-mono text-[0.7rem] text-foreground">
              {draft.source}
            </span>{' '}
            pins {pins.vars} web variable{pins.vars === 1 ? '' : 's'} and{' '}
            {pins.tui} terminal color{pins.tui === 1 ? '' : 's'}. They are kept
            as they were written and win over the roles below, so a role you
            change here may not move on every surface. Tick to drop them and
            derive everything from the roles instead.
          </span>
        </label>
      )}

      {issues.name && <FieldError text={issues.name} />}
      {issues.slug && <FieldError text={issues.slug} />}
      {previewError && (
        <FieldError text={`live preview: ${previewError}`} />
      )}

      <div className="flex flex-wrap items-center gap-2">
        {EDITOR_MODES.map((name) => {
          const defined = modes.includes(name)
          return defined ? (
            <div
              key={name}
              className={cn(
                'inline-flex items-center gap-1 rounded-md border border-border',
                mode === name && 'border-ring bg-secondary/60',
              )}
            >
              <button
                type="button"
                onClick={() => setMode(name)}
                aria-pressed={mode === name}
                className="px-2.5 py-1 font-mono text-xs text-foreground"
              >
                {name}
              </button>
              {modes.length > 1 && (
                <button
                  type="button"
                  onClick={() => {
                    const next = removeMode(draft, name)
                    setDraft(next)
                    if (mode === name) setMode(draftModes(next)[0] ?? 'light')
                  }}
                  aria-label={`Remove the ${name} mode`}
                  title={`Remove the ${name} mode`}
                  className="rounded-r-md px-1.5 py-1 text-faint transition-colors hover:text-fail"
                >
                  <X className="size-3" aria-hidden="true" />
                </button>
              )}
            </div>
          ) : (
            <button
              key={name}
              type="button"
              onClick={() => {
                setDraft(addMode(draft, name, seedRoles(draft, name, themes)))
                setMode(name)
              }}
              className="inline-flex items-center gap-1 rounded-md border border-dashed border-border px-2.5 py-1 font-mono text-xs text-muted-foreground transition-colors hover:text-foreground"
            >
              <Plus className="size-3" aria-hidden="true" />
              add {name}
            </button>
          )
        })}
        <span className="font-mono text-[0.7rem] text-faint">
          a mode this theme does not paint keeps trau's built-in palette
        </span>
      </div>

      <div className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3">
        {CORE_ROLES.map((role) => (
          <RoleInput
            key={role}
            role={role}
            value={draft.modes[mode]?.roles[role] ?? ''}
            error={issues.roles[mode]?.[role]}
            onChange={(value) => setDraft(setRole(draft, mode, role, value))}
          />
        ))}
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <Button
          size="sm"
          onClick={save}
          disabled={blocked || mutation.isPending}
          className="h-8 gap-1.5 font-mono text-xs"
        >
          <Check className="size-3.5" aria-hidden="true" />
          {mutation.isPending ? 'saving…' : 'Save theme'}
        </Button>
        <Button
          variant="outline"
          size="sm"
          onClick={() => {
            previewPalettes(null)
            onCancel()
          }}
          className="h-8 gap-1.5 font-mono text-xs"
        >
          Cancel
        </Button>
        {mutation.error && (
          <FieldError text={(mutation.error as Error).message} />
        )}
      </div>

      <ConfirmDialog
        open={confirmOverwrite}
        onOpenChange={setConfirmOverwrite}
        windowTitle="overwrite theme"
        title={`Replace the saved theme “${slug}”?`}
        description={`A theme is already saved as ${slug}.json. Saving replaces its colors with this draft.`}
        confirmLabel="Replace"
        destructive
        onConfirm={() => {
          setConfirmOverwrite(false)
          mutation.mutate()
        }}
      />
    </div>
  )
}

function RoleInput({
  role,
  value,
  error,
  onChange,
}: {
  role: string
  value: string
  error?: string
  onChange: (value: string) => void
}) {
  const valid = isThemeColor(value)
  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-center gap-2 rounded-md border border-border px-2 py-1.5">
        <input
          type="color"
          value={valid && value.length === 7 ? value : '#000000'}
          onChange={(e) => onChange(e.target.value)}
          aria-label={`${role} color`}
          className="size-7 shrink-0 cursor-pointer rounded border border-border bg-input p-0.5"
        />
        <span className="w-14 shrink-0 font-mono text-[0.7rem] text-muted-foreground">
          {role}
        </span>
        <Input
          type="text"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          aria-label={`${role} hex`}
          spellCheck={false}
          className={cn(
            'h-auto min-w-0 flex-1 px-2 py-1 font-mono text-xs',
            !valid && 'border-fail text-fail',
          )}
        />
      </div>
      {error && (
        <span className="pl-2 font-mono text-[0.7rem] text-fail">{error}</span>
      )}
    </div>
  )
}

function FieldError({ text }: { text: string }) {
  return (
    <p
      className="inline-flex items-center gap-2 font-mono text-xs text-fail"
      role="alert"
    >
      <TriangleAlert className="size-3.5" aria-hidden="true" />
      {text}
    </p>
  )
}
