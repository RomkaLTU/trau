package pipeline

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/state"
)

// panelRunner is one panel member's backend: it records the phase label it was
// called with, optionally waits (honoring cancellation), then either returns an
// error or writes its member verdict file. Sharing labels/mu across members lets a
// test assert every member ran under its own distinct label.
type panelRunner struct {
	id     string
	name   string
	write  bool
	v      verdict
	err    error
	delay  time.Duration
	mu     *sync.Mutex
	labels *[]string
}

func (r panelRunner) Run(ctx context.Context, prompt, label string) (agent.Result, error) {
	r.mu.Lock()
	*r.labels = append(*r.labels, label)
	r.mu.Unlock()
	if r.delay > 0 {
		select {
		case <-time.After(r.delay):
		case <-ctx.Done():
			return agent.Result{}, ctx.Err()
		}
	}
	if r.err != nil {
		return agent.Result{}, r.err
	}
	if r.write {
		_ = writeVerdictFile(verifyMemberPath(r.id, r.name), r.v)
	}
	return agent.Result{}, nil
}

func newPanelPipeline(t *testing.T, panel []Verifier, parallel bool) *Pipeline {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() {
		removeVerifyArtifacts(t, panel[0].Runner.(panelRunner).id, panel)
	})
	return &Pipeline{
		State:         state.NewStore(dir),
		RunsDir:       dir,
		Base:          "main",
		Prefix:        "COD",
		VerifyPanel:   panel,
		PanelPolicy:   "unanimous",
		PanelParallel: parallel,
	}
}

func removeVerifyArtifacts(t *testing.T, id string, panel []Verifier) {
	t.Helper()
	_ = os.Remove(verifyPath(id))
	for _, m := range panel {
		_ = os.Remove(verifyMemberPath(id, m.Name))
	}
}

// TestPanelParallelMergedVerdictMatchesSequential covers the merge-order guard:
// the merged verdict must be identical whether members ran concurrently or one
// at a time, even when they finish out of source order. alpha fails slowly and
// beta fails fast, so completion-order collection would tag dissent "beta,
// alpha"; the position-indexed merge must keep source order "alpha, beta" both
// ways.
func TestPanelParallelMergedVerdictMatchesSequential(t *testing.T) {
	build := func(id string) []Verifier {
		mu := &sync.Mutex{}
		labels := &[]string{}
		return []Verifier{
			{Name: "alpha", Runner: panelRunner{
				id: id, name: "alpha", write: true, delay: 40 * time.Millisecond,
				v:  verdict{Pass: false, Summary: "alpha down", Failures: []string{"alpha broke a thing"}},
				mu: mu, labels: labels,
			}},
			{Name: "beta", Runner: panelRunner{
				id: id, name: "beta", write: true,
				v:  verdict{Pass: false, Summary: "beta down", Failures: []string{"beta broke a thing"}},
				mu: mu, labels: labels,
			}},
		}
	}

	seqPanel := build("COD-79701")
	seq := newPanelPipeline(t, seqPanel, false)
	seqVerdict, err := seq.runPanel(context.Background(), "COD-79701", "verify", "brief", "", "", "", "", "", "", "")
	if err != nil {
		t.Fatalf("sequential runPanel err = %v", err)
	}

	parPanel := build("COD-79701")
	par := newPanelPipeline(t, parPanel, true)
	parVerdict, err := par.runPanel(context.Background(), "COD-79701", "verify", "brief", "", "", "", "", "", "", "")
	if err != nil {
		t.Fatalf("parallel runPanel err = %v", err)
	}

	if !reflect.DeepEqual(seqVerdict, parVerdict) {
		t.Fatalf("parallel verdict %+v != sequential verdict %+v", parVerdict, seqVerdict)
	}
	wantFails := []string{"[alpha] alpha broke a thing", "[beta] beta broke a thing"}
	if !reflect.DeepEqual(parVerdict.Failures, wantFails) {
		t.Errorf("failures = %v, want source-ordered %v", parVerdict.Failures, wantFails)
	}
}

// TestPanelParallelRunsConcurrently covers the concurrency guard: three members
// that each sleep before answering must finish in roughly one member's time, not
// the sum, and each must run under its own distinct member label.
func TestPanelParallelRunsConcurrently(t *testing.T) {
	const delay = 120 * time.Millisecond
	id := "COD-79702"
	mu := &sync.Mutex{}
	labels := &[]string{}
	var panel []Verifier
	for _, name := range []string{"alpha", "beta", "gamma"} {
		panel = append(panel, Verifier{Name: name, Runner: panelRunner{
			id: id, name: name, write: true, delay: delay,
			v:  verdict{Pass: true, Summary: name + " ok"},
			mu: mu, labels: labels,
		}})
	}
	p := newPanelPipeline(t, panel, true)

	start := time.Now()
	v, err := p.runPanel(context.Background(), id, "verify", "brief", "", "", "", "", "", "", "")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("runPanel err = %v", err)
	}
	if !v.Pass {
		t.Errorf("merged verdict = %+v, want pass", v)
	}
	if elapsed >= 3*delay {
		t.Errorf("panel took %s for 3×%s members — did not run concurrently", elapsed, delay)
	}
	if got := append([]string(nil), *labels...); len(got) != 3 {
		t.Fatalf("recorded %d member labels, want 3: %v", len(got), got)
	}
	for _, want := range []string{"verify-alpha", "verify-beta", "verify-gamma"} {
		if !containsLine(*labels, want) {
			t.Errorf("member label %q missing from %v", want, *labels)
		}
	}
}

// TestPanelParallelFatalMemberCancelsAndPropagates covers the fatal-abort guard:
// a provider pause from one member must cancel the still-running members and
// propagate as a *PausedError, so the ticket stays resumable. The surviving
// member sleeps far longer than the pause takes, so a fast return proves it was
// cancelled.
func TestPanelParallelFatalMemberCancelsAndPropagates(t *testing.T) {
	id := "COD-79703"
	mu := &sync.Mutex{}
	labels := &[]string{}
	panel := []Verifier{
		{Name: "alpha", Runner: panelRunner{
			id: id, name: "alpha",
			err: errors.New("kimi run (verify): 429 usage limit reached"),
			mu:  mu, labels: labels,
		}},
		{Name: "beta", Runner: panelRunner{
			id: id, name: "beta", write: true, delay: 5 * time.Second,
			v:  verdict{Pass: true, Summary: "beta ok"},
			mu: mu, labels: labels,
		}},
	}
	p := newPanelPipeline(t, panel, true)

	start := time.Now()
	_, err := p.runPanel(context.Background(), id, "verify", "brief", "", "", "", "", "", "", "")
	elapsed := time.Since(start)

	if !IsPaused(err) {
		t.Fatalf("runPanel err = %v, want a *PausedError", err)
	}
	if elapsed >= 2*time.Second {
		t.Errorf("panel took %s — the surviving member was not cancelled", elapsed)
	}
}

// TestPanelParallelMemberCrashCountsAsFail covers the dissent guard: a plain
// member timeout/crash (not a pause or budget give-up) is folded into that
// member's fail verdict and blocks the unanimous merge, but runPanel still
// returns nil — it is a member dissent, not a phase error.
func TestPanelParallelMemberCrashCountsAsFail(t *testing.T) {
	id := "COD-79704"
	mu := &sync.Mutex{}
	labels := &[]string{}
	panel := []Verifier{
		{Name: "alpha", Runner: panelRunner{
			id: id, name: "alpha", write: true,
			v:  verdict{Pass: true, Summary: "alpha ok"},
			mu: mu, labels: labels,
		}},
		{Name: "beta", Runner: panelRunner{
			id: id, name: "beta", err: errors.New("agent crashed: boom"),
			mu: mu, labels: labels,
		}},
	}
	p := newPanelPipeline(t, panel, true)

	v, err := p.runPanel(context.Background(), id, "verify", "brief", "", "", "", "", "", "", "")

	if err != nil {
		t.Fatalf("runPanel err = %v, want nil (a member crash is a dissent, not a phase error)", err)
	}
	if v.Pass {
		t.Errorf("merged verdict = %+v, want fail (beta crashed)", v)
	}
}
