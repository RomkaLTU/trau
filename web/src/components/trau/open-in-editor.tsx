import { useState } from 'react'
import { Check, ChevronDown, ExternalLink, FolderOpen } from 'lucide-react'

import { Button } from '@/components/ui/button'
import {
  Command,
  CommandGroup,
  CommandItem,
  CommandList,
} from '@/components/ui/command'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { editorLabel, editorUrl, EDITORS, useEditor } from '@/lib/editor'
import { cn } from '@/lib/utils'

// The link is a plain anchor so the browser runs its protocol-handler flow —
// nothing here reaches the hub, which is what keeps it safe on a non-loopback bind.
export function OpenInEditor({ root }: { root: string }) {
  const [editor, setEditor] = useEditor()
  const [open, setOpen] = useState(false)
  const label = editorLabel(editor)

  return (
    <div className="inline-flex items-center">
      <Button
        asChild
        variant="outline"
        size="sm"
        className="rounded-r-none border-r-0 font-mono"
        title={`Open ${root} in ${label}`}
      >
        <a href={editorUrl(editor, root)}>
          <FolderOpen className="size-4" aria-hidden="true" />
          Open in editor
        </a>
      </Button>
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger asChild>
          <Button
            variant="outline"
            size="sm"
            aria-label="Choose editor"
            title={`Editor: ${label}`}
            className="rounded-l-none px-2"
          >
            <ChevronDown className="size-3.5" aria-hidden="true" />
          </Button>
        </PopoverTrigger>
        <PopoverContent align="end" className="w-48 p-0">
          <Command shouldFilter={false}>
            <CommandList>
              <CommandGroup>
                {EDITORS.map((option) => (
                  <CommandItem
                    key={option.id}
                    value={option.id}
                    onSelect={() => {
                      setEditor(option.id)
                      setOpen(false)
                    }}
                  >
                    <Check
                      className={cn(
                        'size-4',
                        option.id === editor ? 'opacity-100' : 'opacity-0',
                      )}
                    />
                    <span className="flex-1 truncate">{option.label}</span>
                  </CommandItem>
                ))}
              </CommandGroup>
            </CommandList>
          </Command>
        </PopoverContent>
      </Popover>
    </div>
  )
}

export function OpenFileInEditor({
  path,
  line,
}: {
  path: string
  line?: number
}) {
  const [editor] = useEditor()

  return (
    <a
      href={editorUrl(editor, path, line)}
      aria-label="Open file in editor"
      title={`Open in ${editorLabel(editor)}`}
      className="shrink-0 rounded-md p-1 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
    >
      <ExternalLink className="size-3.5" aria-hidden="true" />
    </a>
  )
}
