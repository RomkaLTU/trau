package pipeline

import (
	"context"
	"strings"

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
// warns, so it never blocks delivery or the give-up path.
func (p *Pipeline) postQANote(ctx context.Context, id string, note tracker.QANote) {
	if !p.QANotes {
		return
	}
	poster, ok := p.Tracker.(tracker.QANotePoster)
	if !ok {
		return
	}
	if err := poster.PostQANote(ctx, id, note); err != nil {
		p.logf("  ⚠ QA note not posted: %v", err)
	}
}
