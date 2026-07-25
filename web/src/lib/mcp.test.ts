import { describe, expect, it } from 'vitest'

import { mcpEndpoint, mcpSetups } from './mcp'

function snippet(url: string, tokenRequired: boolean, id: string): string {
  const setup = mcpSetups(url, tokenRequired).find((s) => s.id === id)
  if (!setup) throw new Error(`no setup for ${id}`)
  return setup.snippet
}

describe('mcpEndpoint', () => {
  it('appends the endpoint path to the page origin', () => {
    expect(mcpEndpoint('http://127.0.0.1:8728')).toBe(
      'http://127.0.0.1:8728/api/v1/mcp',
    )
  })

  it('does not double the separator on a trailing slash', () => {
    expect(mcpEndpoint('https://box.tailnet.ts.net:8728/')).toBe(
      'https://box.tailnet.ts.net:8728/api/v1/mcp',
    )
  })
})

describe('mcpSetups', () => {
  const url = 'http://127.0.0.1:8728/api/v1/mcp'

  it('gives Claude Code a bare add command on an open hub', () => {
    expect(snippet(url, false, 'claude')).toBe(
      `claude mcp add --transport http trau ${url}`,
    )
  })

  it('adds the Authorization header when the hub is token-gated', () => {
    expect(snippet(url, true, 'claude')).toContain(
      '--header "Authorization: Bearer $TRAU_SERVE_TOKEN"',
    )
  })

  it('writes the Codex server block against the resolved url', () => {
    expect(snippet(url, false, 'codex')).toBe(
      `[mcp_servers.trau]\nurl = "${url}"`,
    )
  })

  it('names the token env var for Codex when gated', () => {
    expect(snippet(url, true, 'codex')).toContain(
      'bearer_token_env_var = "TRAU_SERVE_TOKEN"',
    )
  })

  it('emits parseable .mcp.json with an http server', () => {
    expect(JSON.parse(snippet(url, false, 'json'))).toEqual({
      mcpServers: { trau: { type: 'http', url } },
    })
  })

  it('carries the header into .mcp.json when gated', () => {
    expect(JSON.parse(snippet(url, true, 'json'))).toEqual({
      mcpServers: {
        trau: {
          type: 'http',
          url,
          headers: { Authorization: 'Bearer $TRAU_SERVE_TOKEN' },
        },
      },
    })
  })
})
