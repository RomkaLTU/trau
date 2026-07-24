package webserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
)

// fakeRenderAgent stands in for the browser-harness render session: on Run it writes
// bytes to outPath (the export) when set, and returns a canned final message.
type fakeRenderAgent struct {
	outPath string
	bytes   []byte
	final   string
	runErr  error
	entered chan struct{}
	release chan struct{}
}

func (a *fakeRenderAgent) Run(_ context.Context, _, _ string) (agent.Result, error) {
	if a.entered != nil {
		close(a.entered)
		<-a.release
	}
	if len(a.bytes) > 0 {
		_ = os.WriteFile(a.outPath, a.bytes, 0o644)
	}
	return agent.Result{
		Final: a.final,
		Model: "claude-opus-5",
		Usage: agent.Usage{Input: 100, Output: 50},
	}, a.runErr
}

func fixedRenderRunner(a agent.Runner) func(config.Config, registry.Repo) (agent.Runner, error) {
	return func(config.Config, registry.Repo) (agent.Runner, error) { return a, nil }
}

// seedVideoProof stores a video proof row plus one screenshot for a run, returning
// the screenshot digest so a test can assert it survives a video replacement.
func seedVideoProof(t *testing.T, s *Server, repo registry.Repo, ticket, traceDir string) string {
	t.Helper()
	shot, _, err := s.stores.Proofs().Blobs().Put(strings.NewReader("shot"), 0)
	if err != nil {
		t.Fatalf("seed screenshot: %v", err)
	}
	if err := s.stores.Proofs().Replace(repo.Root, ticket, []hubstore.RunProof{
		{Seq: 0, Kind: hubstore.ProofVideo, TraceDir: traceDir, CreatedAt: "t0"},
		{Seq: 1, Kind: hubstore.ProofScreenshot, SHA256: shot, Mime: "image/png", Caption: "login", TraceDir: traceDir, CreatedAt: "t0"},
	}); err != nil {
		t.Fatalf("seed proofs: %v", err)
	}
	return shot
}

func TestVideoRenderHappyPath(t *testing.T) {
	s, repo := atlasServer(t)
	const ticket = "COD-1"
	trace := t.TempDir()
	shot := seedVideoProof(t, s, repo, ticket, trace)

	mp4 := []byte("\x00\x00\x00\x18ftypmp42 rendered clip")
	s.render.newRunner = fixedRenderRunner(&fakeRenderAgent{outPath: videoOutputPath(repo.Root, ticket), bytes: mp4})

	video, _, _ := s.stores.Proofs().Video(repo.Root, ticket)
	s.render.render(context.Background(), repo, ticket, video)

	if got := s.render.state(repo.Root, ticket); got.status != renderDone {
		t.Fatalf("render state = %+v, want done", got)
	}
	rows, err := s.stores.Proofs().ForRun(repo.Root, ticket)
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("proofs = %d rows, want the video plus the preserved screenshot", len(rows))
	}
	if rows[0].Kind != hubstore.ProofVideo || rows[0].SHA256 == "" || rows[0].Mime != videoMime {
		t.Errorf("video row = %+v, want the rendered mp4 bytes", rows[0])
	}
	if rows[1].SHA256 != shot {
		t.Errorf("screenshot row = %+v, want it preserved", rows[1])
	}

	f, err := s.stores.Proofs().Blobs().Open(rows[0].SHA256)
	if err != nil {
		t.Fatalf("open rendered video: %v", err)
	}
	_ = f.Close()

	spend, err := s.stores.Tokens().Total(repo.Root, videoBucket)
	if err != nil {
		t.Fatalf("token total: %v", err)
	}
	if spend.Tokens != 150 {
		t.Errorf("video token spend = %d, want 150", spend.Tokens)
	}
}

func TestVideoRenderNoOutputFails(t *testing.T) {
	s, repo := atlasServer(t)
	const ticket = "COD-2"
	trace := t.TempDir()
	seedVideoProof(t, s, repo, ticket, trace)

	s.render.newRunner = fixedRenderRunner(&fakeRenderAgent{final: "the recording does not match ticket COD-2"})

	video, _, _ := s.stores.Proofs().Video(repo.Root, ticket)
	s.render.render(context.Background(), repo, ticket, video)

	got := s.render.state(repo.Root, ticket)
	if got.status != renderFailed {
		t.Fatalf("render state = %+v, want failed", got)
	}
	if got.reason == "" || got.reason == videoAuthFailure {
		t.Errorf("failure reason = %q, want the agent's explanation", got.reason)
	}
	vid, _, _ := s.stores.Proofs().Video(repo.Root, ticket)
	if vid.SHA256 != "" {
		t.Errorf("video row gained bytes on a failed render: %+v", vid)
	}
}

func TestVideoRenderTracePrunedFails(t *testing.T) {
	s, repo := atlasServer(t)
	const ticket = "COD-3"
	pruned := t.TempDir() + "/gone"
	seedVideoProof(t, s, repo, ticket, pruned)

	ran := false
	s.render.newRunner = fixedRenderRunner(runnerFunc(func() { ran = true }))

	video, _, _ := s.stores.Proofs().Video(repo.Root, ticket)
	s.render.render(context.Background(), repo, ticket, video)

	if ran {
		t.Error("render spawned an agent for a pruned trace; want it to fail before spawning")
	}
	got := s.render.state(repo.Root, ticket)
	if got.status != renderFailed || got.reason == "" {
		t.Fatalf("render state = %+v, want a failure naming the pruned trace", got)
	}
}

func TestVideoRenderConcurrencyGuard(t *testing.T) {
	s, repo := atlasServer(t)
	const ticket = "COD-4"
	trace := t.TempDir()
	video := hubstore.RunProof{Kind: hubstore.ProofVideo, TraceDir: trace}
	seedVideoProof(t, s, repo, ticket, trace)

	blocking := &fakeRenderAgent{
		outPath: videoOutputPath(repo.Root, ticket),
		bytes:   []byte("clip"),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	s.render.newRunner = fixedRenderRunner(blocking)

	if !s.render.start(repo, ticket, video) {
		t.Fatal("first start returned false, want the render to begin")
	}
	<-blocking.entered
	if s.render.start(repo, ticket, video) {
		t.Error("second start returned true while a render was in flight, want a no-op")
	}
	if got := s.render.state(repo.Root, ticket); got.status != renderRendering {
		t.Errorf("state = %+v, want rendering while in flight", got)
	}

	close(blocking.release)
	waitRenderIdle(t, s, repo.Root, ticket)
	if got := s.render.state(repo.Root, ticket); got.status != renderDone {
		t.Errorf("state after settle = %+v, want done", got)
	}
}

func TestRenderVideoHandler(t *testing.T) {
	s, repo := atlasServer(t)
	trace := t.TempDir()
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	post := func(ticket string) *http.Response {
		return doReq(t, http.MethodPost, ts.URL+APIPrefix+"/repos/acme/runs/"+ticket+"/render-video", nil)
	}

	res := post("COD-missing")
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("render with no recording = %d, want 422", res.StatusCode)
	}
	_ = res.Body.Close()

	seedVideoProof(t, s, repo, "COD-5", t.TempDir()+"/gone")
	res = post("COD-5")
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("render with pruned trace = %d, want 422", res.StatusCode)
	}
	_ = res.Body.Close()

	seedVideoProof(t, s, repo, "COD-6", trace)
	blocking := &fakeRenderAgent{
		outPath: videoOutputPath(repo.Root, "COD-6"),
		bytes:   []byte("clip"),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	s.render.newRunner = fixedRenderRunner(blocking)

	res = post("COD-6")
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("render with a live trace = %d, want 202", res.StatusCode)
	}
	_ = res.Body.Close()
	<-blocking.entered

	res = post("COD-6")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("second render while in flight = %d, want 409", res.StatusCode)
	}
	_ = res.Body.Close()

	close(blocking.release)
	waitRenderIdle(t, s, repo.Root, "COD-6")
}

func TestProofViewSurfacesTraceAndRenderState(t *testing.T) {
	s, repo := atlasServer(t)
	trace := t.TempDir()
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	seedVideoProof(t, s, repo, "COD-7", trace)
	if got := videoRow(t, ts, "COD-7"); !got.TraceExists || got.URL != "" {
		t.Errorf("live-trace video row = %+v, want trace_exists and no bytes URL yet", got)
	}

	seedVideoProof(t, s, repo, "COD-8", trace+"/gone")
	if got := videoRow(t, ts, "COD-8"); got.TraceExists {
		t.Errorf("pruned-trace video row = %+v, want trace_exists false", got)
	}

	s.render.settle(renderKey{repo: repo.Root, ticket: "COD-7"}, renderJob{status: renderFailed, reason: "boom"})
	got := videoRow(t, ts, "COD-7")
	if got.Render != string(renderFailed) || got.RenderError != "boom" {
		t.Errorf("video row render state = %+v, want the failure surfaced", got)
	}
}

func videoRow(t *testing.T, ts *httptest.Server, ticket string) ProofView {
	t.Helper()
	res, body := get(t, ts, APIPrefix+"/repos/acme/runs/"+ticket+"/proofs")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET proofs = %d, want 200", res.StatusCode)
	}
	var views []ProofView
	if err := json.Unmarshal([]byte(body), &views); err != nil {
		t.Fatalf("decode proofs: %v", err)
	}
	for _, v := range views {
		if v.Kind == hubstore.ProofVideo {
			return v
		}
	}
	t.Fatalf("no video row in proofs list for %s", ticket)
	return ProofView{}
}

func waitRenderIdle(t *testing.T, s *Server, repoRoot, ticket string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.render.state(repoRoot, ticket).status != renderRendering {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("render did not settle")
}

type runnerFunc func()

func (f runnerFunc) Run(context.Context, string, string) (agent.Result, error) {
	f()
	return agent.Result{}, nil
}
