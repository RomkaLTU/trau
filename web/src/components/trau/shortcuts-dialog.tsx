import { KbdHint } from '@/components/trau/kbd-hint'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import { isMacPlatform } from '@/lib/palette-keys'
import { keyLabel, shortcutSections } from '@/lib/shortcuts'

// ShortcutsDialog is the discoverability surface behind ?. It renders the
// registry and nothing else.
export function ShortcutsDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const mac = isMacPlatform(navigator)
  const sections = shortcutSections()

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        aria-describedby={undefined}
        className="gap-0 overflow-hidden border-border bg-popover p-0 shadow-xl sm:max-w-xl"
      >
        <div className="flex items-center gap-3 border-b border-border px-4 py-2.5">
          <div className="flex items-center gap-1.5" aria-hidden="true">
            <span className="size-2.5 rounded-full bg-fail" />
            <span className="size-2.5 rounded-full bg-warn" />
            <span className="size-2.5 rounded-full bg-done" />
          </div>
          <span className="font-mono text-xs text-muted-foreground">
            shortcuts
          </span>
        </div>

        <div className="flex max-h-[70vh] flex-col gap-5 overflow-y-auto px-4 py-4">
          <DialogTitle className="font-mono text-sm font-normal text-foreground">
            Keyboard shortcuts
          </DialogTitle>

          {sections.map((section) => (
            <section
              key={section.group}
              data-slot="shortcut-section"
              className="flex flex-col gap-1"
            >
              <p className="px-2 pb-1 font-mono text-[0.65rem] uppercase tracking-[0.2em] text-muted-foreground">
                {section.group}
              </p>
              <ul className="flex flex-col">
                {section.items.map((shortcut) => (
                  <li
                    key={`${section.group}-${shortcut.keys.join('-')}`}
                    data-slot="shortcut-row"
                    className="flex items-center gap-3 rounded-md px-2 py-1.5 transition-colors hover:bg-secondary"
                  >
                    <KbdHint
                      keys={shortcut.keys.map((key) => keyLabel(key, mac))}
                    />
                    <span className="min-w-0 flex-1 truncate font-mono text-sm text-foreground">
                      {shortcut.label}
                    </span>
                    <span className="shrink-0 font-mono text-xs text-muted-foreground">
                      {shortcut.where}
                    </span>
                  </li>
                ))}
              </ul>
            </section>
          ))}
        </div>
      </DialogContent>
    </Dialog>
  )
}
