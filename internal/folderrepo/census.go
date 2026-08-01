package folderrepo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sync/errgroup"
)

// SweepConcurrency bounds the parallel git calls a child sweep spends. A sweep is
// a couple of cheap reads per child, but a folder holds around fifty of them and
// process spawn — not git — is what a serial sweep would wait on.
const SweepConcurrency = 8

// Sweep reads one value per Child repo, concurrently but bounded, and returns
// them in child order. Every read is a couple of cheap git calls, so one that
// fails carries its own zero value rather than cancelling the rest.
func Sweep[T any](ctx context.Context, children []Child, read func(context.Context, Child) T) []T {
	out := make([]T, len(children))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(SweepConcurrency)
	for i, c := range children {
		g.Go(func() error {
			out[i] = read(gctx, c)
			return nil
		})
	}
	_ = g.Wait()
	return out
}

// State is a Child repo's condition when a sweep read it: the branch it sits on
// and the Fingerprint of whatever uncommitted work its tree carries, or the error
// that stopped either from being read.
type State struct {
	Child
	Branch string
	Dirt   string
	Err    error
}

// OffLimitsReason names why a run must leave this child alone, or "" when it may
// ship to it. The doctor's preview and the start-of-run sweep both judge a child
// with it, so what doctor shows cannot drift from the verdict a run reaches.
func (s State) OffLimitsReason(base string) string {
	switch {
	case s.Err != nil, s.Branch == "":
		return "its git state could not be read"
	case s.Dirt != "":
		return "it has uncommitted changes"
	case s.Branch != base:
		return "it sits on " + s.Branch + ", not " + base
	}
	return ""
}

// carries answers whether this child still holds the work a run left in it: its
// tree no longer reads as the start-of-run census found it, or it sits on the
// branch the run committed to. A child that could not be read stays as the census
// found it.
func (s State) carries(start map[string]string, branch string) bool {
	switch {
	case s.Err != nil:
		return false
	case branch != "" && s.Branch == branch:
		return true
	}
	return s.Dirt != start[s.Name]
}

// ReadState reads a Child repo's condition by running git in it, for the callers
// outside a run — the doctor's preview, the hub's diff pane — that hold no git
// seam of their own. A run reads its children through its own.
func ReadState(ctx context.Context, c Child) State {
	st := State{Child: c}
	branch, err := gitOutput(ctx, c.Path, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		st.Err = err
		return st
	}
	st.Branch = branch
	status, err := gitOutput(ctx, c.Path, "status", "--porcelain")
	if err != nil {
		st.Err = err
		return st
	}
	st.Dirt = Fingerprint(c.Path, status)
	return st
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return "", fmt.Errorf("git %s in %s: %w", strings.Join(args, " "), dir, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Carrying names the Child repos still holding a run's work, judged against the
// census the run started from: the ones whose tree no longer reads as it did
// then, which is what tells a run's own work from whatever an operator left
// there. branch also counts a child sitting on it — work already committed there
// leaves the tree reading clean again — and a caller whose run has committed
// nothing anywhere passes "".
func Carrying(ctx context.Context, children []Child, start map[string]string, branch string, read func(context.Context, Child) State) []Child {
	out := make([]Child, 0, len(children))
	for i, st := range Sweep(ctx, children, read) {
		if st.carries(start, branch) {
			out = append(out, children[i])
		}
	}
	return out
}

// Fingerprint condenses a Child repo's porcelain status — untracked files
// included — into a digest two readings can be compared by, so a stray file an
// operator left behind cannot read as clean to one reading and as a run's own
// work to the next. A clean tree fingerprints as "".
//
// Each named path's size and mtime go into the digest because the status line
// alone does not move when a file that already read as dirty is written again:
// without them, a build editing the very file an operator left behind would slip
// past the off-limits guard.
func Fingerprint(dir, status string) string {
	if status == "" {
		return ""
	}
	sum := sha256.New()
	for _, line := range strings.Split(status, "\n") {
		var size, modified int64
		if fi, err := os.Stat(filepath.Join(dir, statusPath(line))); err == nil {
			size, modified = fi.Size(), fi.ModTime().UnixNano()
		}
		fmt.Fprintf(sum, "%s\x00%d\x00%d\x00", line, size, modified)
	}
	return hex.EncodeToString(sum.Sum(nil))[:16]
}

// statusPath is the working-tree path a porcelain status line names: what follows
// the two status columns, or the arrow when the entry is a rename. A path this
// misreads simply contributes no size or mtime — both readings misread it alike.
func statusPath(line string) string {
	path := strings.TrimSpace(line)
	if len(line) > 3 {
		path = strings.TrimSpace(line[3:])
	}
	if _, renamed, ok := strings.Cut(path, " -> "); ok {
		path = renamed
	}
	return strings.Trim(path, `"`)
}

// FormatCensus renders a per-child census as name=value entries. The separator is
// "; " because a reason naming the branch a child sits on carries a comma of its own.
func FormatCensus(census map[string]string) string {
	entries := make([]string, 0, len(census))
	for _, name := range SortedNames(census) {
		entries = append(entries, name+"="+census[name])
	}
	return strings.Join(entries, "; ")
}

// ParseCensus reads back what FormatCensus wrote.
func ParseCensus(recorded string) map[string]string {
	census := map[string]string{}
	for _, entry := range strings.Split(recorded, "; ") {
		if name, value, ok := strings.Cut(entry, "="); ok {
			census[name] = value
		}
	}
	return census
}

// SortedNames lists a census's Child repo names in order.
func SortedNames(census map[string]string) []string {
	names := make([]string, 0, len(census))
	for name := range census {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
