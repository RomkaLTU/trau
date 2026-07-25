import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  Check,
  Copy,
  ExternalLink,
  ShieldCheck,
  TriangleAlert,
} from 'lucide-react'

import { SegmentedControl } from '@/components/trau/segmented-control'
import { TerminalCard } from '@/components/trau/terminal-card'
import { Button } from '@/components/ui/button'
import { copyText } from '@/lib/clipboard'
import { healthQueryOptions } from '@/lib/health'
import {
  MCP_DOCS_URL,
  MCP_SCOPE_SUMMARY,
  mcpEndpoint,
  mcpSetups,
  type MCPClientID,
} from '@/lib/mcp'
import { cn } from '@/lib/utils'

export function MCPConnectSection() {
  const health = useQuery(healthQueryOptions)
  const [client, setClient] = useState<MCPClientID>('claude')

  const url = mcpEndpoint(window.location.origin)
  const tokenRequired = health.data?.token_required ?? false
  const setups = mcpSetups(url, tokenRequired)
  const active = setups.find((s) => s.id === client) ?? setups[0]

  return (
    <section id="external-agents" className="scroll-mt-6">
      <TerminalCard
        title="external agents (mcp)"
        bodyClassName="flex flex-col gap-4 p-4"
      >
        <p className="text-xs leading-relaxed text-muted-foreground">
          Point Claude Code, Codex or Cursor at this hub and it drives trau over
          MCP: {MCP_SCOPE_SUMMARY}.
        </p>

        <div className="flex flex-wrap items-center gap-2">
          <span className="font-mono text-xs text-faint">endpoint</span>
          <code className="min-w-0 flex-1 truncate rounded-md border border-border bg-input px-2.5 py-1.5 font-mono text-xs text-foreground">
            {url}
          </code>
          <CopyButton value={url} label="Copy endpoint URL" />
        </div>

        {tokenRequired && (
          <p className="inline-flex items-start gap-2 rounded-md border border-warn/40 bg-warn/[0.08] px-2.5 py-2 text-xs leading-relaxed text-muted-foreground">
            <ShieldCheck
              className="mt-0.5 size-3.5 shrink-0 text-warn"
              aria-hidden="true"
            />
            <span>
              This hub is exposed, so every request must carry the serve token.
              Export it as{' '}
              <code className="font-mono text-foreground">
                TRAU_SERVE_TOKEN
              </code>{' '}
              before pasting the snippet.
            </span>
          </p>
        )}

        <div className="flex flex-wrap items-center gap-3">
          <SegmentedControl
            aria-label="MCP client"
            options={setups.map((s) => ({ value: s.id, label: s.label }))}
            value={active.id}
            onChange={setClient}
          />
          <span className="font-mono text-xs text-faint">{active.target}</span>
        </div>

        <div className="relative">
          <pre className="overflow-x-auto rounded-md border border-border bg-input p-3 pr-12 font-mono text-xs leading-relaxed text-foreground">
            {active.snippet}
          </pre>
          <CopyButton
            value={active.snippet}
            label={`Copy ${active.label} snippet`}
            className="absolute right-2 top-2"
          />
        </div>

        <a
          href={MCP_DOCS_URL}
          target="_blank"
          rel="noreferrer"
          className="inline-flex items-center gap-1.5 self-start font-mono text-xs text-primary underline-offset-2 hover:underline"
        >
          the full tool list and worked examples
          <ExternalLink className="size-3.5" aria-hidden="true" />
        </a>
      </TerminalCard>
    </section>
  )
}

const COPY_FEEDBACK = {
  idle: { icon: Copy, tone: '', status: '' },
  copied: { icon: Check, tone: 'text-done', status: 'copied' },
  failed: {
    icon: TriangleAlert,
    tone: 'text-destructive',
    status: 'copy failed',
  },
} as const

function CopyButton({
  value,
  label,
  className,
}: {
  value: string
  label: string
  className?: string
}) {
  const [state, setState] = useState<keyof typeof COPY_FEEDBACK>('idle')
  const { icon: Icon, tone, status } = COPY_FEEDBACK[state]

  useEffect(() => {
    if (state === 'idle') return
    const timer = setTimeout(() => setState('idle'), 1600)
    return () => clearTimeout(timer)
  }, [state])

  return (
    <Button
      type="button"
      variant="outline"
      size="icon-sm"
      aria-label={label}
      className={cn('shrink-0', className)}
      onClick={() => {
        void copyText(value).then((ok) => setState(ok ? 'copied' : 'failed'))
      }}
    >
      <Icon className={cn('size-3.5', tone)} aria-hidden="true" />
      <span className="sr-only" role="status">
        {status}
      </span>
    </Button>
  )
}
