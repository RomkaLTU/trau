export const MCP_ENDPOINT_PATH = '/api/v1/mcp'

export const MCP_SCOPE_SUMMARY =
  'file tickets, run the queue, watch runs, steer agents — full operator control'

export const MCP_DOCS_URL =
  'https://github.com/RomkaLTU/trau/blob/main/docs/external-agents.md'

const TOKEN_ENV = 'TRAU_SERVE_TOKEN'

export type MCPClientID = 'claude' | 'codex' | 'json'

export interface MCPSetup {
  id: MCPClientID
  label: string
  target: string
  snippet: string
}

// Built from the origin serving this page, so the URL is right for however the
// user reached the hub — loopback, LAN address, tailnet name.
export function mcpEndpoint(origin: string): string {
  return `${origin.replace(/\/+$/, '')}${MCP_ENDPOINT_PATH}`
}

export function mcpSetups(url: string, tokenRequired: boolean): MCPSetup[] {
  return [
    {
      id: 'claude',
      label: 'Claude Code',
      target: 'run it in any project',
      snippet: claudeSnippet(url, tokenRequired),
    },
    {
      id: 'codex',
      label: 'Codex',
      target: '~/.codex/config.toml',
      snippet: codexSnippet(url, tokenRequired),
    },
    {
      id: 'json',
      label: '.mcp.json',
      target: 'Cursor and most other clients',
      snippet: jsonSnippet(url, tokenRequired),
    },
  ]
}

function claudeSnippet(url: string, tokenRequired: boolean): string {
  const add = `claude mcp add --transport http trau ${url}`
  if (!tokenRequired) return add
  return `${add} \\\n  --header "Authorization: Bearer $${TOKEN_ENV}"`
}

function codexSnippet(url: string, tokenRequired: boolean): string {
  const block = `[mcp_servers.trau]\nurl = "${url}"`
  if (!tokenRequired) return block
  return `${block}\nbearer_token_env_var = "${TOKEN_ENV}"`
}

function jsonSnippet(url: string, tokenRequired: boolean): string {
  const server: Record<string, unknown> = { type: 'http', url }
  if (tokenRequired) {
    server.headers = { Authorization: `Bearer $${TOKEN_ENV}` }
  }
  return JSON.stringify({ mcpServers: { trau: server } }, null, 2)
}
