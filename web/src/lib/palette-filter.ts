// cmdk's own filtering is off so server-ranked issue rows keep the hub's order,
// which leaves the palette's static rows to be matched here.
export function matchesQuery(query: string, terms: readonly string[]): boolean {
  const tokens = query.toLowerCase().split(/\s+/).filter(Boolean)
  if (tokens.length === 0) return true
  const haystack = terms.join(' ').toLowerCase()
  return tokens.every((token) => haystack.includes(token))
}
