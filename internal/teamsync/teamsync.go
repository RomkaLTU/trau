// Package teamsync carries a machine's shared trau records to its teammates over
// the target repo's existing git remote — no server, no new credentials. Each
// machine publishes one parentless snapshot commit under refs/trau/team/<writer-id>
// and reads its teammates' snapshots back from the same namespace. Every operation
// touches refs only, never the working tree, so a sync is safe while a loop is
// building.
package teamsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	// PayloadVersion is the snapshot schema this build publishes and accepts. A
	// payload from any other version is skipped rather than guessed at.
	PayloadVersion = 1

	// RefPrefix namespaces every writer's published snapshot on the remote.
	RefPrefix = "refs/trau/team/"

	// TrackingPrefix is where a fetch mirrors the teammates' refs locally.
	TrackingPrefix = "refs/trau/remotes/team/"

	payloadFile = "payload.json"
	writerIDLen = 16
)

// gitWaitDelay bounds Wait once the context has killed git. A networked git forks
// a transport (ssh, git-remote-https) the kill does not reach, which keeps the
// output pipe open long after the deadline would otherwise have returned.
const gitWaitDelay = 2 * time.Second

// Config identifies the target repo and how to reach the remote its teammates
// share. An empty Remote means origin, an empty GitBin means git on PATH.
type Config struct {
	RepoDir string
	Remote  string
	GitBin  string
}

// Writer is the identity a machine publishes under: the ref segment plus the git
// identity teammates attribute its records to. ID is stable per (identity,
// machine), so one person working on two machines publishes two refs that both
// name them.
type Writer struct {
	ID    string
	Name  string
	Email string
}

// Lesson is one shared lessons-ledger record on the wire, mirroring the ledger's
// own field names so a published payload stays readable independently of the hub
// schema it came out of.
type Lesson struct {
	Ticket       string   `json:"ticket,omitempty"`
	Phase        string   `json:"phase,omitempty"`
	FailureType  string   `json:"failure_type,omitempty"`
	AttemptedFix string   `json:"attempted_fix,omitempty"`
	Evidence     []string `json:"evidence,omitempty"`
	Result       string   `json:"result,omitempty"`
	Lesson       string   `json:"lesson"`
	Tags         []string `json:"tags,omitempty"`
	RecordedAt   string   `json:"recorded_at,omitempty"`
}

// ActivitySpan is one Activity's wall-clock inside a shared run — the whole
// timeline that travels, in place of the event stream it was derived from.
type ActivitySpan struct {
	Activity   string `json:"activity"`
	DurationMS int64  `json:"duration_ms"`
}

// Run is one settled run on the wire — the summary a teammate's ledger shows for
// it. Transcripts, PTY and phase logs, and raw event streams are deliberately
// absent: they are megabytes per run, and every snapshot the remote has ever
// carried is a commit somebody has to keep.
type Run struct {
	Ticket        string         `json:"ticket"`
	Title         string         `json:"title,omitempty"`
	Repo          string         `json:"repo,omitempty"`
	Branch        string         `json:"branch,omitempty"`
	PRURL         string         `json:"pr_url,omitempty"`
	Phase         string         `json:"phase,omitempty"`
	FailureClass  string         `json:"failure_class,omitempty"`
	FailureReason string         `json:"failure_reason,omitempty"`
	StartedAt     string         `json:"started_at,omitempty"`
	EndedAt       string         `json:"ended_at,omitempty"`
	Provider      string         `json:"provider,omitempty"`
	Model         string         `json:"model,omitempty"`
	Tokens        int            `json:"tokens,omitempty"`
	CostUSD       *float64       `json:"cost_usd,omitempty"`
	Activities    []ActivitySpan `json:"activities,omitempty"`
}

// Snapshot is what a machine shares: its own lessons ledger and its own recent
// settled runs. A teammate's records are never re-exported. Runs is absent from a
// snapshot published before shared history existed, which stays a readable
// PayloadVersion 1 payload rather than one to skip.
type Snapshot struct {
	Lessons []Lesson `json:"lessons"`
	Runs    []Run    `json:"runs,omitempty"`
}

// Payload is one writer's published snapshot: who wrote it, when it was published,
// and the records they share. Only a machine's own records are ever published — a
// teammate's records are never re-exported.
type Payload struct {
	Version   int    `json:"version"`
	Writer    string `json:"writer"`
	Name      string `json:"name,omitempty"`
	Email     string `json:"email,omitempty"`
	UpdatedAt string `json:"updated_at"`
	Snapshot
}

// WriterID derives the ref segment an email publishes under on a machine: a short
// hash of the two, keeping the segment stable, opaque, and safe inside a ref name.
func WriterID(email, machineID string) string {
	seed := strings.ToLower(strings.TrimSpace(email)) + "\x00" + strings.TrimSpace(machineID)
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])[:writerIDLen]
}

// MachineID names this machine for writer-id derivation. An unavailable hostname
// falls back to a constant, merging that machine's ref with any other unnamed
// one — acceptable for an opt-in, best-effort transport.
func MachineID() string {
	host, err := os.Hostname()
	if strings.TrimSpace(host) == "" || err != nil {
		return "unknown"
	}
	return host
}

// ResolveWriter reads the repo's effective git identity — the name and email git
// itself would stamp a commit with — and derives this machine's writer id from it.
// A repo with no configured identity still yields a usable id from the machine.
func ResolveWriter(ctx context.Context, cfg Config) Writer {
	email := cfg.configValue(ctx, "user.email")
	return Writer{
		ID:    WriterID(email, MachineID()),
		Name:  cfg.configValue(ctx, "user.name"),
		Email: email,
	}
}

// HasRemote reports whether the repo has the remote team sync would publish to.
// Without one nothing is ever attempted.
func HasRemote(ctx context.Context, cfg Config) bool {
	_, err := cfg.git(ctx, nil, nil, "remote", "get-url", cfg.remote())
	return err == nil
}

// Push publishes snap as a parentless snapshot commit and force-updates the
// writer's ref on the remote. Snapshot-only keeps remote growth bounded: each push
// orphans the previous commit for the host's own GC instead of chaining history.
func Push(ctx context.Context, cfg Config, w Writer, snap Snapshot) error {
	if snap.Lessons == nil {
		snap.Lessons = []Lesson{}
	}
	data, err := json.MarshalIndent(Payload{
		Version:   PayloadVersion,
		Writer:    w.ID,
		Name:      w.Name,
		Email:     w.Email,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Snapshot:  snap,
	}, "", "  ")
	if err != nil {
		return err
	}
	blob, err := cfg.git(ctx, nil, bytes.NewReader(data), "hash-object", "-w", "--stdin")
	if err != nil {
		return err
	}
	entry := "100644 blob " + blob + "\t" + payloadFile + "\n"
	tree, err := cfg.git(ctx, nil, bytes.NewReader([]byte(entry)), "mktree")
	if err != nil {
		return err
	}
	commit, err := cfg.git(ctx, commitEnv(w), nil, "commit-tree", tree, "-m", "trau team sync")
	if err != nil {
		return err
	}
	_, err = cfg.git(ctx, nil, nil, "push", "--force", cfg.remote(), commit+":"+RefPrefix+w.ID)
	return err
}

// Fetch mirrors every published teammate ref into local tracking storage and
// returns the payloads it could read, excluding the machine's own writer id. A ref
// whose payload is missing, malformed, or from another schema version is skipped
// rather than failing the pass — one bad writer never blinds the rest.
func Fetch(ctx context.Context, cfg Config, self string) ([]Payload, error) {
	refspec := "+" + RefPrefix + "*:" + TrackingPrefix + "*"
	if _, err := cfg.git(ctx, nil, nil, "fetch", "--prune", "--force", cfg.remote(), refspec); err != nil {
		return nil, err
	}
	listed, err := cfg.git(ctx, nil, nil, "for-each-ref", "--format=%(refname)", TrackingPrefix)
	if err != nil {
		return nil, err
	}
	payloads := []Payload{}
	for _, ref := range strings.Split(listed, "\n") {
		ref = strings.TrimSpace(ref)
		writer := strings.TrimPrefix(ref, TrackingPrefix)
		if ref == "" || writer == self {
			continue
		}
		p, ok := cfg.readPayload(ctx, ref)
		if !ok {
			continue
		}
		// The ref name is what the remote actually accepted, so it — not the
		// payload body — decides who a snapshot is attributed to.
		p.Writer = writer
		payloads = append(payloads, p)
	}
	return payloads, nil
}

func (c Config) readPayload(ctx context.Context, ref string) (Payload, bool) {
	out, err := c.git(ctx, nil, nil, "cat-file", "blob", ref+":"+payloadFile)
	if err != nil {
		return Payload{}, false
	}
	var p Payload
	if err := json.Unmarshal([]byte(out), &p); err != nil || p.Version != PayloadVersion {
		return Payload{}, false
	}
	return p, true
}

func (c Config) configValue(ctx context.Context, key string) string {
	out, err := c.git(ctx, nil, nil, "config", "--get", key)
	if err != nil {
		return ""
	}
	return out
}

// commitEnv stamps the snapshot commit with the writer's identity, so the commit
// succeeds even where git has no identity of its own to auto-detect.
func commitEnv(w Writer) []string {
	name, email := w.Name, w.Email
	if name == "" {
		name = "trau"
	}
	if email == "" {
		email = "trau@localhost"
	}
	return []string{
		"GIT_AUTHOR_NAME=" + name,
		"GIT_AUTHOR_EMAIL=" + email,
		"GIT_COMMITTER_NAME=" + name,
		"GIT_COMMITTER_EMAIL=" + email,
	}
}

func (c Config) git(ctx context.Context, env []string, stdin *bytes.Reader, args ...string) (string, error) {
	full := append([]string{"-C", c.RepoDir}, args...)
	cmd := exec.CommandContext(ctx, c.gitBin(), full...)
	cmd.WaitDelay = gitWaitDelay
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
