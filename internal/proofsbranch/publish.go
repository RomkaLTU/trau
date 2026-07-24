// Package proofsbranch publishes a run's verify screenshots to a dedicated
// orphan branch (trau-proofs) in the target repo, so the delivered PR can embed
// visual QA proof without any new infrastructure. It never touches the feature
// branch, main, or the working tree: commits are assembled with git plumbing
// against a throwaway index and the resulting commit is pushed straight to the
// remote branch ref.
package proofsbranch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Branch is the orphan branch every repo's proofs live on. It is never merged;
// each run adds its screenshots under a <ticket>/ directory.
const Branch = "trau-proofs"

const readme = `# trau proofs

This branch stores browser-QA screenshots captured during automated delivery.
Each run's proofs live under a ` + "`<ticket>/`" + ` directory and are referenced
from that ticket's pull request. The branch is orphaned and never merged.
`

// Config identifies the target repo and the tooling used to reach it.
type Config struct {
	RepoDir string
	Remote  string
	GitBin  string
	GHBin   string
}

// Proof is one screenshot to publish, as fetched back from the hub: its seq and
// mime (which name the on-branch file) plus its caption and bytes.
type Proof struct {
	Seq     int
	Mime    string
	Caption string
	Bytes   []byte
}

// Publication reports how the PR body should reference a run's published proofs:
// the owner/repo and branch that host them, whether the repo is private (which
// decides inline images vs links), and each file's on-branch path and caption.
type Publication struct {
	Owner   string
	Repo    string
	Branch  string
	Private bool
	Files   []File
}

// File is one published proof: its path under the branch and the caption to show.
type File struct {
	Path    string
	Caption string
}

// Publish commits proofs to the orphan branch under <ticket>/ and pushes it,
// returning how the PR body should reference them. A repo with no remote, or one
// gh cannot resolve (a push-only mirror), yields an empty Publication and no
// branch write. A git or push failure returns an error the caller logs and
// swallows — proofs never block delivery.
func Publish(ctx context.Context, cfg Config, ticket string, proofs []Proof) (Publication, error) {
	if len(proofs) == 0 {
		return Publication{}, nil
	}
	if _, err := cfg.git(ctx, nil, nil, "remote", "get-url", cfg.remote()); err != nil {
		return Publication{}, nil
	}
	owner, repo, private, ok := cfg.repoInfo(ctx)
	if !ok {
		return Publication{}, nil
	}

	exists, err := cfg.remoteBranchExists(ctx, cfg.remote(), Branch)
	if err != nil {
		return Publication{}, err
	}
	pl := buildPlan(ticket, exists, proofs)

	commit, err := cfg.commitPlan(ctx, ticket, pl, proofs)
	if err != nil {
		return Publication{}, err
	}
	if _, err := cfg.git(ctx, nil, nil, "push", cfg.remote(), commit+":refs/heads/"+Branch); err != nil {
		return Publication{}, err
	}
	return Publication{Owner: owner, Repo: repo, Branch: Branch, Private: private, Files: pl.Files}, nil
}

// plan is the set of commits Publish will make: a README bootstrap commit when
// the branch is new, plus the index entries for this run's screenshots.
type plan struct {
	Bootstrap bool
	Files     []File
}

// buildPlan decides whether the branch must be bootstrapped and lays out each
// screenshot's on-branch path and caption in proof order.
func buildPlan(ticket string, branchExists bool, proofs []Proof) plan {
	files := make([]File, 0, len(proofs))
	for _, p := range proofs {
		files = append(files, File{
			Path:    ticket + "/" + filename(p.Seq, p.Mime),
			Caption: caption(p, ticket),
		})
	}
	return plan{Bootstrap: !branchExists, Files: files}
}

// commitPlan builds the proofs commit against a throwaway index and returns its
// sha. When bootstrapping it first commits a README as the branch's root, then
// parents the proofs commit on it; otherwise it parents on the fetched branch tip.
func (c Config) commitPlan(ctx context.Context, ticket string, pl plan, proofs []Proof) (string, error) {
	tmp, err := os.MkdirTemp("", "trau-proofs-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "index")}

	parent := ""
	if pl.Bootstrap {
		blob, err := c.hashObject(ctx, []byte(readme))
		if err != nil {
			return "", err
		}
		if err := c.addToIndex(ctx, env, blob, "README.md"); err != nil {
			return "", err
		}
		if parent, err = c.commitTree(ctx, env, "", "proofs: initialize "+Branch); err != nil {
			return "", err
		}
	} else {
		if _, err := c.git(ctx, nil, nil, "fetch", c.remote(), Branch); err != nil {
			return "", err
		}
		parent, err = c.git(ctx, nil, nil, "rev-parse", "FETCH_HEAD")
		if err != nil {
			return "", err
		}
		if _, err := c.git(ctx, env, nil, "read-tree", parent); err != nil {
			return "", err
		}
	}

	for i, p := range proofs {
		blob, err := c.hashObject(ctx, p.Bytes)
		if err != nil {
			return "", err
		}
		if err := c.addToIndex(ctx, env, blob, pl.Files[i].Path); err != nil {
			return "", err
		}
	}
	return c.commitTree(ctx, env, parent, "proofs: "+ticket)
}

func (c Config) commitTree(ctx context.Context, env []string, parent, msg string) (string, error) {
	tree, err := c.git(ctx, env, nil, "write-tree")
	if err != nil {
		return "", err
	}
	args := []string{"commit-tree", tree, "-m", msg}
	if parent != "" {
		args = []string{"commit-tree", tree, "-p", parent, "-m", msg}
	}
	return c.git(ctx, nil, nil, args...)
}

func (c Config) addToIndex(ctx context.Context, env []string, blob, path string) error {
	_, err := c.git(ctx, env, nil, "update-index", "--add", "--cacheinfo", "100644,"+blob+","+path)
	return err
}

func (c Config) hashObject(ctx context.Context, data []byte) (string, error) {
	return c.git(ctx, nil, bytes.NewReader(data), "hash-object", "-w", "--stdin")
}

func (c Config) remoteBranchExists(ctx context.Context, remote, branch string) (bool, error) {
	out, err := c.git(ctx, nil, nil, "ls-remote", "--heads", remote, branch)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func (c Config) repoInfo(ctx context.Context) (owner, repo string, private, ok bool) {
	out, err := c.gh(ctx, "repo", "view", "--json", "nameWithOwner,isPrivate")
	if err != nil {
		return "", "", false, false
	}
	var v struct {
		NameWithOwner string `json:"nameWithOwner"`
		IsPrivate     bool   `json:"isPrivate"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return "", "", false, false
	}
	owner, repo, ok = strings.Cut(v.NameWithOwner, "/")
	if !ok || owner == "" || repo == "" {
		return "", "", false, false
	}
	return owner, repo, v.IsPrivate, true
}

func (c Config) git(ctx context.Context, env []string, stdin *bytes.Reader, args ...string) (string, error) {
	full := append([]string{"-C", c.RepoDir}, args...)
	cmd := exec.CommandContext(ctx, c.gitBin(), full...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func (c Config) gh(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.ghBin(), args...)
	cmd.Dir = c.RepoDir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (c Config) remote() string {
	if c.Remote != "" {
		return c.Remote
	}
	return "origin"
}

func (c Config) gitBin() string {
	if c.GitBin != "" {
		return c.GitBin
	}
	return "git"
}

func (c Config) ghBin() string {
	if c.GHBin != "" {
		return c.GHBin
	}
	return "gh"
}

// filename names a screenshot on the branch from its seq and mime, so the same
// proof lands on a stable path across reruns.
func filename(seq int, mime string) string {
	name := "proof-" + strconv.Itoa(seq)
	if ext := imageExt(mime); ext != "" {
		return name + ext
	}
	return name
}

func caption(p Proof, ticket string) string {
	if c := strings.TrimSpace(p.Caption); c != "" {
		return c
	}
	return ticket + " " + filename(p.Seq, p.Mime)
}

func imageExt(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ""
	}
}
