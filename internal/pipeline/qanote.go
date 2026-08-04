package pipeline

import (
	"context"
	"path"
	"strings"

	"github.com/RomkaLTU/trau/internal/proofsbranch"
	"github.com/RomkaLTU/trau/internal/sanitize"
	"github.com/RomkaLTU/trau/internal/tracker"
)

const qaNoteHeading = "## Trau QA report"

// deliveryQANote is the report a delivered slice leaves on its ticket: the same
// verify facts the PR body's Testing section states, plus the PR the run opened.
func (p *Pipeline) deliveryQANote(ctx context.Context, id, prURL string) tracker.QANote {
	lines := append([]string{qaNoteHeading, ""}, p.testingLines(ctx, id)...)
	lines = append(lines, "", "PR: "+prURL)
	return qaNoteOf(lines)
}

// failureQANote is the report a run leaves when it gives up: the failed verdict's
// summary and failure lines, plus the HITL blocker FileBug opened, if any.
func failureQANote(v verdict, bug string) tracker.QANote {
	failed := "Verify failed"
	if s := sanitize.FeedLine(v.Summary); s != "" {
		failed += ": " + s
	}
	lines := []string{qaNoteHeading, "", failed, ""}
	for _, fl := range strings.Split(v.failureLines(), "\n") {
		if s := sanitize.FeedLine(fl); s != "" {
			lines = append(lines, "- "+s)
		}
	}
	if bug != "" {
		lines = append(lines, "", "HITL blocker: "+bug)
	}
	return qaNoteOf(lines)
}

func qaNoteOf(lines []string) tracker.QANote {
	return tracker.QANote{Body: strings.Join(lines, "\n") + "\n"}
}

// postQANote comments the report on the ticket, best-effort: a failed post only
// warns, so it never blocks delivery or the give-up path. pub is the run's proofs
// publication when it reached one — the give-up path publishes nothing and passes
// the zero value.
func (p *Pipeline) postQANote(ctx context.Context, id string, note tracker.QANote, pub proofsbranch.Publication) {
	if !p.QANotes {
		return
	}
	poster, ok := p.Tracker.(tracker.QANotePoster)
	if !ok {
		return
	}
	note.Images = p.qaImages(ctx, id, pub)
	if err := poster.PostQANote(ctx, id, note); err != nil {
		p.logf("  ⚠ QA note not posted: %v", err)
	}
}

// qaImages reads the run's verify screenshots back for the note: bytes for the
// providers that upload natively, plus the trau-proofs URLs of whichever ones this
// run published so the link-only providers embed exactly what the PR body does. A
// failed fetch warns and yields no images — the text note still posts.
func (p *Pipeline) qaImages(ctx context.Context, id string, pub proofsbranch.Publication) []tracker.QAImage {
	if p.FetchProofs == nil {
		return nil
	}
	proofs, err := p.FetchProofs(ctx, id)
	if err != nil {
		p.logf("  ⚠ QA note screenshots unavailable: %v", err)
		return nil
	}
	published := make(map[string]string, len(pub.Files))
	for _, f := range pub.Files {
		published[path.Base(f.Path)] = f.Path
	}
	images := make([]tracker.QAImage, 0, len(proofs))
	for _, pr := range proofs {
		img := tracker.QAImage{
			Name:    proofsbranch.Filename(pr.Seq, pr.Mime),
			Mime:    pr.Mime,
			Caption: sanitize.FeedLine(proofsbranch.Caption(pr, id)),
			Bytes:   pr.Bytes,
		}
		if onBranch, ok := published[img.Name]; ok {
			if pub.Private {
				img.BlobURL = proofBlobURL(pub, onBranch)
			} else {
				img.RawURL = proofRawURL(pub, onBranch)
			}
		}
		images = append(images, img)
	}
	return images
}
