package webserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
)

// videoRenderTimeout bounds one render end to end. It is a var so tests can shorten
// it.
var videoRenderTimeout = 30 * time.Minute

const (
	// videoMaxBytes caps a stored render. A narrated walkthrough is a short clip, so
	// this is a backstop against a runaway export, not a normal limit.
	videoMaxBytes = 256 << 20

	videoMime    = "video/mp4"
	videoCaption = "Narrated verify walkthrough"

	// videoBucket is the token-ledger ticket a render's spend is attributed to,
	// mirroring the loop's _loop bucket so it never lands in a real ticket's run
	// breakdown. videoPhase is the cost category the Costs page groups it under.
	videoBucket = "_video"
	videoPhase  = "video"

	videoAuthFailure = "the render agent needs re-authentication — re-login (run the provider CLI, then /login), then try again"
)

// renderStatus is a render job's lifecycle state, surfaced to the run page.
type renderStatus string

const (
	renderIdle      renderStatus = ""
	renderRendering renderStatus = "rendering"
	renderFailed    renderStatus = "failed"
	renderDone      renderStatus = "done"
)

// renderKey is a render's identity: one in-flight job per run.
type renderKey struct {
	repo   string
	ticket string
}

// renderJob is a run's latest render state — its status and, on failure, the reason
// the run page shows.
type renderJob struct {
	status renderStatus
	reason string
}

// videoRenderer is the process side of on-demand video render: a hub-spawned,
// one-shot agent session that follows the browser-harness make-video workflow
// against a run's already-recorded trace directory (never re-enacting browser work),
// then stores the exported mp4 as the run's video proof. One render per run at a
// time; a second request while one is in flight is refused.
type videoRenderer struct {
	srv     *Server
	baseCtx context.Context

	newRunner   func(cfg config.Config, repo registry.Repo) (agent.Runner, error)
	traceExists func(dir string) bool
	now         func() time.Time

	mu   sync.Mutex
	jobs map[renderKey]renderJob
}

func newVideoRenderer(s *Server) *videoRenderer {
	return &videoRenderer{
		srv:         s,
		baseCtx:     context.Background(),
		newRunner:   newHubRunner,
		traceExists: traceDirExists,
		now:         time.Now,
		jobs:        map[renderKey]renderJob{},
	}
}

// EnableVideoRender binds render to ctx — the hub's lifetime — so cancelling it stops
// any render in flight. Call it once, before serving.
func (s *Server) EnableVideoRender(ctx context.Context) {
	s.render.baseCtx = ctx
}

// start launches a render for the run in the background and reports whether it
// started. It returns false when a render for the same run is already in flight, so
// the handler can answer 409. The turn is bounded by the hub context, not the
// request's, so it outlives the POST that began it.
func (v *videoRenderer) start(repo registry.Repo, ticket string, video hubstore.RunProof) bool {
	key := renderKey{repo: repo.Root, ticket: ticket}
	v.mu.Lock()
	if v.jobs[key].status == renderRendering {
		v.mu.Unlock()
		return false
	}
	v.jobs[key] = renderJob{status: renderRendering}
	v.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(v.baseCtx, videoRenderTimeout)
		defer cancel()
		v.render(ctx, repo, ticket, video)
	}()
	return true
}

// state returns a run's latest render state — idle, rendering, failed with a reason,
// or done.
func (v *videoRenderer) state(repoRoot, ticket string) renderJob {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.jobs[renderKey{repo: repoRoot, ticket: ticket}]
}

func (v *videoRenderer) settle(key renderKey, job renderJob) {
	v.mu.Lock()
	v.jobs[key] = job
	v.mu.Unlock()
}

// render runs the session to a stored outcome: it re-checks the trace still exists,
// spawns the render agent against it, reads the exported mp4, and stores it as the
// run's video proof — or records the reason it could not. The session's token spend
// is recorded as the video cost category regardless of outcome.
func (v *videoRenderer) render(ctx context.Context, repo registry.Repo, ticket string, video hubstore.RunProof) {
	key := renderKey{repo: repo.Root, ticket: ticket}
	if video.TraceDir == "" || !v.traceExists(video.TraceDir) {
		v.fail(key, "the recording was pruned — its trace directory is no longer on disk")
		return
	}

	projectPath, userPath := v.srv.repoConfigPaths(repo)
	cfg, err := config.LoadLayered(projectPath, userPath, "", "")
	if err != nil {
		v.fail(key, "could not load the repository config: "+err.Error())
		return
	}
	runner, err := v.newRunner(cfg, repo)
	if err != nil {
		v.fail(key, "could not start the render agent: "+err.Error())
		return
	}

	out := videoOutputPath(repo.Root, ticket)
	_ = os.Remove(out)
	res, runErr := runner.Run(ctx, videoRenderPrompt(ticket, video.TraceDir, out), videoPhase)
	recordAgentSpend(v.srv, repo.Root, videoBucket, videoPhase, cfg.Provider, res, v.now())
	defer func() { _ = os.Remove(out) }()

	if errors.Is(runErr, agent.ErrAuthRequired) {
		v.fail(key, videoAuthFailure)
		return
	}
	data, err := os.ReadFile(out)
	if err != nil || len(data) == 0 {
		v.fail(key, videoFailure(runErr, res))
		return
	}

	sha, _, err := v.srv.stores.Proofs().Blobs().Put(bytes.NewReader(data), videoMaxBytes)
	if err != nil {
		reason := "could not store the rendered video: " + err.Error()
		if errors.Is(err, hubstore.ErrAttachmentTooLarge) {
			reason = "the rendered video is too large to store"
		}
		v.fail(key, reason)
		return
	}
	row := hubstore.RunProof{
		Seq:       0,
		Kind:      hubstore.ProofVideo,
		SHA256:    sha,
		Mime:      videoMime,
		Caption:   videoCaption,
		TraceDir:  video.TraceDir,
		CreatedAt: v.now().UTC().Format(time.RFC3339Nano),
	}
	if err := v.srv.stores.Proofs().SetVideo(repo.Root, ticket, row); err != nil {
		v.fail(key, "could not store the rendered video: "+err.Error())
		return
	}
	v.settle(key, renderJob{status: renderDone})
}

func (v *videoRenderer) fail(key renderKey, reason string) {
	v.settle(key, renderJob{status: renderFailed, reason: reason})
	logger.Verbosef("video render %s/%s: %s", key.repo, key.ticket, reason)
}

// traceDirExists reports whether dir is a directory on the local disk — the hub runs
// on the same machine as the recorder, so it can stat the recorded path directly.
func traceDirExists(dir string) bool {
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// videoOutputPath is the deterministic per-run temp path the render agent exports the
// mp4 to and the hub reads it back from. The digest keeps the name filesystem-safe
// regardless of the ticket's characters and distinct across runs.
func videoOutputPath(repoRoot, ticket string) string {
	sum := sha256.Sum256([]byte(repoRoot + "\x00" + ticket))
	return filepath.Join(os.TempDir(), "trau-video-"+hex.EncodeToString(sum[:8])+".mp4")
}

// videoFailure phrases why a render produced no mp4, for the stored failure the run
// page shows. It prefers the agent's own explanation, falling back to the run error.
func videoFailure(runErr error, res agent.Result) string {
	if final := firstLine(res.Final); final != "" {
		return "the render agent produced no video: " + final
	}
	if runErr != nil {
		return "the render agent failed: " + runErr.Error()
	}
	return "the render agent produced no video"
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	const max = 300
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

// videoRenderPrompt instructs a one-shot agent to turn an existing verify recording
// into a narrated mp4 with the browser-harness make-video workflow, exporting to out.
// It is post-production only — the agent must never open a browser or re-enact work,
// and must fail rather than substitute a different recording.
func videoRenderPrompt(ticket, traceDir, out string) string {
	return fmt.Sprintf(`Produce a short narrated walkthrough video of the already-completed browser QA verification for ticket %[1]s, using the browser-harness post-production workflow. This is pure post-production over an existing recording: never open a browser, never re-run or re-enact any browser work, and never substitute a different recording.

Use the browser-harness skill and operate strictly on this recording directory:

  %[2]s

Steps:
1. Confirm the recording belongs to this ticket by inspecting its meta.json and events.jsonl. If they are missing or clearly belong to a different task, STOP and do not export — report that the recording does not match ticket %[1]s, and why.
2. browser-harness video init %[2]s --require-explicit
3. Write %[2]s/edit-brief.json per the make-video workflow: one causal chain of intent → action → visible result, narration only where it changes, verified outcomes.
4. browser-harness video review %[2]s — inspect video-review-contact-sheet.jpg and every image under .privacy-review/. Keep typed text hidden (do not set showTyping) and redact any credentials, tokens, tenant data, private URLs, or unrelated people with opaque rectangles listed in the brief's privacy block.
5. browser-harness video export %[2]s --reviewed --output %[3]s

Write the finished mp4 to exactly:

  %[3]s

When done, state whether the export succeeded and the output path. If you cannot produce the video for any reason, say so plainly and do not fabricate footage.`, ticket, traceDir, out)
}
