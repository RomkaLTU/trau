package registry

// CanonicalRoot returns the single spelling of a repo root the hub stores, keys,
// and compares by. Two paths naming one directory have to reduce to one string or
// the repo lists twice with its flags split across the rows: the web registers
// `C:\Users\x\y` while the same repo's heartbeat reports the `git rev-parse`
// spelling `C:/Users/x/y`. An empty root stays empty rather than becoming Clean's
// ".", so a caller can still tell a missing root from a real one.
func CanonicalRoot(root string) string {
	if root == "" {
		return ""
	}
	return canonicalRoot(root)
}
