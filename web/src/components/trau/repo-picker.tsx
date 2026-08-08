import { useState } from 'react'
import { Check, ChevronDown } from 'lucide-react'

import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from '@/components/ui/command'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { cn } from '@/lib/utils'

export function RepoPicker({
  repos,
  value,
  onChange,
  label = 'repo',
  className,
}: {
  repos: readonly string[]
  value: string
  onChange: (repo: string) => void
  label?: string
  className?: string
}) {
  const [open, setOpen] = useState(false)

  return (
    <div className={cn('flex flex-col gap-1.5', className)}>
      {label ? (
        <span className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
          {label}
        </span>
      ) : null}
      <div className="w-48">
        <Popover open={open} onOpenChange={setOpen}>
          <PopoverTrigger asChild>
            <button
              type="button"
              aria-label={label || undefined}
              className="flex w-full items-center justify-between rounded-md border border-border bg-input px-2.5 py-1.5 font-mono text-sm text-foreground hover:border-ring/50"
            >
              <span className="truncate">{value || '—'}</span>
              <ChevronDown
                className="size-3.5 shrink-0 text-muted-foreground"
                aria-hidden="true"
              />
            </button>
          </PopoverTrigger>
          <PopoverContent
            align="start"
            className="w-(--radix-popover-trigger-width) min-w-48 p-0"
          >
            <Command>
              <CommandInput placeholder="Search…" />
              <CommandList>
                <CommandEmpty>No match.</CommandEmpty>
                <CommandGroup>
                  {repos.map((repo) => (
                    <CommandItem
                      key={repo}
                      value={repo}
                      onSelect={() => {
                        setOpen(false)
                        onChange(repo)
                      }}
                    >
                      <Check
                        className={cn(
                          'size-4',
                          repo === value ? 'opacity-100' : 'opacity-0',
                        )}
                      />
                      <span className="flex-1 truncate font-mono">{repo}</span>
                    </CommandItem>
                  ))}
                </CommandGroup>
              </CommandList>
            </Command>
          </PopoverContent>
        </Popover>
      </div>
    </div>
  )
}
