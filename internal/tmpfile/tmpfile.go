// Package tmpfile resolves the paths of the loop's agent-interface files — the
// QA briefs, verdicts, rubrics, build notes and materialized attachments the
// phases hand to each other, which live outside the target repository's working
// tree so nothing an agent writes for the next phase can land in a commit.
// Resolving them through os.TempDir() keeps them valid on hosts without a /tmp,
// Windows among them (ADR 0023).
package tmpfile

import (
	"os"
	"path/filepath"
)

// Path names an agent-interface file or directory inside the OS temp directory.
func Path(name string) string { return filepath.Join(os.TempDir(), name) }
