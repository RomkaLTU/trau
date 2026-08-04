package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/proofsbranch"
	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// qaTracker is a fakeTracker that can also post QA notes, recording every one so a
// run's comment count and content are observable.
type qaTracker struct {
	*fakeTracker
	notes   []tracker.QANote
	postErr error
}

func newQATracker() *qaTracker { return &qaTracker{fakeTracker: &fakeTracker{}} }

func (t *qaTracker) PostQANote(_ context.Context, _ string, note tracker.QANote) error {
	t.notes = append(t.notes, note)
	return t.postErr
}

const qaNotePR = "https://github.test/pr/91"

// newQADeliveryPipeline is a pipeline whose CommitAndPR reaches a real PR URL, with
// a passing verdict on disk for the report to state.
func newQADeliveryPipeline(t *testing.T, tr tracker.Tracker, id string) *Pipeline {
	t.Helper()
	git := &localGit{hasRemote: true, branch: "feature/" + id + "-slice"}
	gh := &countingGitHub{epicGitHub: epicGitHub{createURL: qaNotePR}}
	p := localTestPipeline(t, git, gh, tr)
	p.QANotes = true
	p.AppURL = "http://app.test"
	writeSliceVerdict(t, id, verdict{
		Pass:    true,
		Summary: "the panel renders and the totals add up",
		Checks:  []checkResult{{Name: "tests", Pass: true}, {Name: "lint", Pass: true}},
		Browser: "driven",
	})
	return p
}

// A delivered slice leaves exactly one comment on its ticket, stating the same
// verify facts the PR body does plus the PR the run opened.
func TestDeliveryPostsOneQANoteWithTheVerifyFactsAndPRLink(t *testing.T) {
	id := "COD-91426"
	tr := newQATracker()
	p := newQADeliveryPipeline(t, tr, id)

	if err := p.CommitAndPR(context.Background(), id); err != nil {
		t.Fatalf("CommitAndPR = %v, want nil", err)
	}
	if len(tr.notes) != 1 {
		t.Fatalf("posted %d QA notes, want exactly 1", len(tr.notes))
	}
	body := tr.notes[0].Body
	for _, want := range []string{
		qaNoteHeading,
		"Verify passed: the panel renders and the totals add up",
		"Verify checks: tests passed, lint passed",
		"Browser QA: driven against http://app.test",
		"PR: " + qaNotePR,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("QA note is missing %q:\n%s", want, body)
		}
	}
	if len(tr.notes[0].Images) != 0 {
		t.Errorf("QA note carried %d images with no proofs fetch wired, want none", len(tr.notes[0].Images))
	}
}

// qaProofs is a run's harvested screenshots: one captioned by the verifier, one
// left to the default naming.
func qaProofs(context.Context, string) ([]proofsbranch.Proof, error) {
	return []proofsbranch.Proof{
		{Seq: 1, Mime: "image/png", Caption: "home", Bytes: []byte("png-1")},
		{Seq: 2, Mime: "image/png", Bytes: []byte("png-2")},
	}, nil
}

// A delivered run's note carries every verify screenshot: the bytes a native-upload
// provider needs, plus the trau-proofs URL a link-only provider embeds — inline on
// a public repo, clickable on a private one.
func TestDeliveryQANoteCarriesProofBytesAndPublishedURLs(t *testing.T) {
	const id = "COD-91431"
	cases := []struct {
		name     string
		private  bool
		wantRaw  string
		wantBlob string
	}{
		{"public repo", false, "https://raw.githubusercontent.com/acme/web/trau-proofs/COD-91431/proof-1.png", ""},
		{"private repo", true, "", "https://github.com/acme/web/blob/trau-proofs/COD-91431/proof-1.png"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := newQATracker()
			p := newQADeliveryPipeline(t, tr, id)
			p.FetchProofs = qaProofs
			p.PublishProofs = func(context.Context, string, string) (proofsbranch.Publication, error) {
				return proofsbranch.Publication{
					Owner:   "acme",
					Repo:    "web",
					Branch:  proofsbranch.Branch,
					Private: tc.private,
					Files: []proofsbranch.File{
						{Path: id + "/proof-1.png", Caption: "home"},
						{Path: id + "/proof-2.png", Caption: id + " proof-2.png"},
					},
				}, nil
			}

			if err := p.CommitAndPR(context.Background(), id); err != nil {
				t.Fatalf("CommitAndPR = %v, want nil", err)
			}
			if len(tr.notes) != 1 {
				t.Fatalf("posted %d QA notes, want exactly 1", len(tr.notes))
			}
			images := tr.notes[0].Images
			if len(images) != 2 {
				t.Fatalf("QA note carried %d images, want both screenshots", len(images))
			}
			want := tracker.QAImage{
				Name:    "proof-1.png",
				Mime:    "image/png",
				Caption: "home",
				Bytes:   []byte("png-1"),
				RawURL:  tc.wantRaw,
				BlobURL: tc.wantBlob,
			}
			if got := images[0]; got.Name != want.Name || got.Mime != want.Mime || got.Caption != want.Caption ||
				string(got.Bytes) != string(want.Bytes) || got.RawURL != want.RawURL || got.BlobURL != want.BlobURL {
				t.Errorf("first image = %+v, want %+v", got, want)
			}
			if got := images[1].Caption; got != id+" proof-2.png" {
				t.Errorf("uncaptioned proof reads %q, want the ticket and its filename", got)
			}
		})
	}
}

// On the terminal-failure path nothing is published, so the note carries the bytes
// a native-upload provider can still show and no URLs at all.
func TestTerminalFailureQANoteCarriesProofBytesWithoutURLs(t *testing.T) {
	id := "COD-91432"
	writeHandoff(t, id)
	tr := newQATracker()
	runner := &verdictRunner{path: verifyPath(id), v: verdict{Summary: "the dashboard stayed blank"}}
	p := newTestPipeline(t, runner, tr)
	p.QANotes = true
	p.FetchProofs = qaProofs

	var giveUp *GiveUpError
	if err := p.Verify(context.Background(), id); !errors.As(err, &giveUp) {
		t.Fatalf("Verify err = %v, want a *GiveUpError", err)
	}
	if len(tr.notes) != 1 {
		t.Fatalf("posted %d QA notes, want exactly 1", len(tr.notes))
	}
	images := tr.notes[0].Images
	if len(images) != 2 {
		t.Fatalf("QA note carried %d images, want both screenshots", len(images))
	}
	for _, img := range images {
		if len(img.Bytes) == 0 {
			t.Errorf("%s carries no bytes, want the screenshot itself", img.Name)
		}
		if img.RawURL != "" || img.BlobURL != "" {
			t.Errorf("%s claims URLs %q/%q, want none — nothing was published", img.Name, img.RawURL, img.BlobURL)
		}
	}
}

// Screenshots are best-effort: a failed fetch still leaves the text report on the
// ticket rather than costing the run its note.
func TestQANoteProofsFetchFailureStillPostsTheTextNote(t *testing.T) {
	id := "COD-91433"
	tr := newQATracker()
	p := newQADeliveryPipeline(t, tr, id)
	p.FetchProofs = func(context.Context, string) ([]proofsbranch.Proof, error) {
		return nil, errors.New("hub unreachable")
	}

	if err := p.CommitAndPR(context.Background(), id); err != nil {
		t.Fatalf("CommitAndPR = %v, want nil", err)
	}
	if len(tr.notes) != 1 {
		t.Fatalf("posted %d QA notes, want exactly 1", len(tr.notes))
	}
	if len(tr.notes[0].Images) != 0 {
		t.Errorf("QA note carried %d images after a failed fetch, want none", len(tr.notes[0].Images))
	}
	if !strings.Contains(tr.notes[0].Body, "PR: "+qaNotePR) {
		t.Errorf("QA note lost its report:\n%s", tr.notes[0].Body)
	}
}

// QA_NOTES=0 suppresses the delivery comment entirely.
func TestQANotesOffPostsNothingOnDelivery(t *testing.T) {
	id := "COD-91427"
	tr := newQATracker()
	p := newQADeliveryPipeline(t, tr, id)
	p.QANotes = false

	if err := p.CommitAndPR(context.Background(), id); err != nil {
		t.Fatalf("CommitAndPR = %v, want nil", err)
	}
	if len(tr.notes) != 0 {
		t.Errorf("posted %d QA notes with QA_NOTES off, want 0", len(tr.notes))
	}
}

// The report is best-effort: a tracker that rejects the comment must not cost the
// run its delivery.
func TestQANotePostFailureNeverFailsDelivery(t *testing.T) {
	id := "COD-91428"
	tr := newQATracker()
	tr.postErr = errors.New("comment endpoint unavailable")
	p := newQADeliveryPipeline(t, tr, id)

	if err := p.CommitAndPR(context.Background(), id); err != nil {
		t.Fatalf("CommitAndPR = %v, want the failed comment ignored", err)
	}
	if got := p.State.Get(id, "PHASE"); got != state.PROpen {
		t.Errorf("PHASE = %q, want %q", got, state.PROpen)
	}
	if got := p.State.Get(id, "PR_URL"); got != qaNotePR {
		t.Errorf("PR_URL = %q, want the opened PR recorded", got)
	}
}

// A tracker without the comment capability is a silent no-op, not an error.
func TestDeliverySkipsQANoteWithoutTheCapability(t *testing.T) {
	id := "COD-91429"
	p := newQADeliveryPipeline(t, &fakeTracker{}, id)

	if err := p.CommitAndPR(context.Background(), id); err != nil {
		t.Fatalf("CommitAndPR = %v, want nil", err)
	}
	if got := p.State.Get(id, "PHASE"); got != state.PROpen {
		t.Errorf("PHASE = %q, want %q", got, state.PROpen)
	}
}

// A run that gives up after its repair and bugfix attempts leaves one comment
// carrying the verdict's failure lines and the HITL blocker it filed.
func TestTerminalFailurePostsQANoteWithFailureLinesAndBug(t *testing.T) {
	id := "COD-91430"
	writeHandoff(t, id)
	tr := newQATracker()
	runner := &verdictRunner{path: verifyPath(id), v: verdict{
		Summary:  "the dashboard stayed blank",
		Failures: []string{"panel renders nothing", "totals endpoint 500s"},
	}}
	p := newTestPipeline(t, runner, tr)
	p.QANotes = true

	err := p.Verify(context.Background(), id)

	var giveUp *GiveUpError
	if !errors.As(err, &giveUp) {
		t.Fatalf("Verify err = %v, want a *GiveUpError", err)
	}
	if len(tr.notes) != 1 {
		t.Fatalf("posted %d QA notes, want exactly 1", len(tr.notes))
	}
	body := tr.notes[0].Body
	for _, want := range []string{
		qaNoteHeading,
		"Verify failed: the dashboard stayed blank",
		"- panel renders nothing",
		"- totals endpoint 500s",
		"HITL blocker: BUG-1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("QA note is missing %q:\n%s", want, body)
		}
	}
}
