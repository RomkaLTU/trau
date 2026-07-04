package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
)

// PlanSlice is one drafted child issue of a published plan's epic, surfaced to
// the slice review list. After references earlier slices by title — the drafts
// carry no identifiers until they are created.
type PlanSlice struct {
	Title       string
	Description string
	Labels      []string
	After       []string
}

// SliceOutcome reports what confirming the reviewed slices did with the tracker:
// the epic they were created under, the created child identifiers in review
// order (on a failure, the ones made before it), and whether anything was
// created at all (a tracker that cannot create child issues skips gracefully).
type SliceOutcome struct {
	Epic     string
	Children []string
	Created  bool
}

// sliceReview is the slice-draft review state behind the Plan screen's planSlices
// step: a cursor list in the run-once picker's style whose rows can be retitled,
// dropped, and reordered before anything is created. Dropping is a toggle so a
// slip of the finger is reversible — dropped rows stay visible but greyed, and
// only kept() filters them out (pruning references to them) when the review
// confirms.
type sliceReview struct {
	epic    string
	rows    []sliceRow
	cursor  int
	editing bool
	input   textinput.Model
	err     string
	// created names the children a failed confirm already made on the tracker;
	// unlike err it survives further edits, so re-confirming — which would
	// duplicate them — is an informed choice.
	created []string
}

type sliceRow struct {
	slice   PlanSlice
	dropped bool
}

func newSliceReview(epic string, slices []PlanSlice) sliceReview {
	rows := make([]sliceRow, len(slices))
	for i, s := range slices {
		rows[i] = sliceRow{slice: s}
	}
	return sliceReview{epic: epic, rows: rows}
}

// move steps the cursor by delta, clamped to the list.
func (r *sliceReview) move(delta int) {
	r.cursor += delta
	if r.cursor < 0 {
		r.cursor = 0
	}
	if r.cursor >= len(r.rows) {
		r.cursor = len(r.rows) - 1
	}
}

// moveRow swaps the cursored slice with its neighbour, the cursor following it,
// so a held reorder key walks the row through the list.
func (r *sliceReview) moveRow(delta int) {
	to := r.cursor + delta
	if to < 0 || to >= len(r.rows) {
		return
	}
	r.rows[r.cursor], r.rows[to] = r.rows[to], r.rows[r.cursor]
	r.cursor = to
	r.err = ""
}

// toggleDrop flips the cursored slice between kept and dropped.
func (r *sliceReview) toggleDrop() {
	r.rows[r.cursor].dropped = !r.rows[r.cursor].dropped
	r.err = ""
}

// startEdit opens the inline title editor on the cursored slice.
func (r *sliceReview) startEdit() {
	ti := textinput.New()
	ti.CharLimit = 200
	ti.SetWidth(48)
	ti.Prompt = "› "
	ti.SetValue(r.rows[r.cursor].slice.Title)
	ti.Focus()
	r.input = ti
	r.editing = true
	r.err = ""
}

// commitEdit applies the typed title and renames every "after" reference that
// pointed at the old one, so retitling never dangles a dependency. An empty or
// unchanged title leaves the row as it was.
func (r *sliceReview) commitEdit() {
	r.editing = false
	title := strings.TrimSpace(r.input.Value())
	old := r.rows[r.cursor].slice.Title
	if title == "" || title == old {
		return
	}
	r.rows[r.cursor].slice.Title = title
	for i := range r.rows {
		for j, ref := range r.rows[i].slice.After {
			if strings.TrimSpace(ref) == old {
				r.rows[i].slice.After[j] = title
			}
		}
	}
}

func (r *sliceReview) cancelEdit() { r.editing = false }

// kept returns the reviewed drafts in list order with dropped rows removed and
// any "after" references to them pruned — the payload a confirm creates.
func (r sliceReview) kept() []PlanSlice {
	keptTitle := make(map[string]bool, len(r.rows))
	for _, row := range r.rows {
		if !row.dropped {
			keptTitle[strings.TrimSpace(row.slice.Title)] = true
		}
	}
	var out []PlanSlice
	for _, row := range r.rows {
		if row.dropped {
			continue
		}
		s := row.slice
		var after []string
		for _, ref := range s.After {
			if keptTitle[strings.TrimSpace(ref)] {
				after = append(after, ref)
			}
		}
		s.After = after
		out = append(out, s)
	}
	return out
}

// view renders the review list: numbered rows in the run-once cursor style, a
// state column carrying each row's dependencies or its dropped flag, and under
// the list any inline error plus the children a failed confirm already created.
func (r sliceReview) view(s Styles, width int) string {
	intro := s.Subtle.Render(fmt.Sprintf("Review the drafted slices — confirming creates them as children of %s. Nothing exists until then.", r.epic))
	rows := []string{intro, ""}
	titleW := width - 30
	if titleW < 16 {
		titleW = 16
	}
	for i, row := range r.rows {
		focused := i == r.cursor
		num := fmt.Sprintf("%d. ", i+1)
		if r.editing && focused {
			rows = append(rows, cursorMarker(s, true)+s.Header.Render(num)+r.input.View())
			continue
		}
		titleStyle, descStyle := s.Subtle, s.Help
		if focused {
			titleStyle, descStyle = s.Header, s.Subtle
		}
		desc := sliceRowDesc(row)
		if row.dropped {
			titleStyle, descStyle = s.Help, s.Help
		}
		line := cursorMarker(s, focused) + titleStyle.Render(num+truncate(row.slice.Title, titleW))
		if desc != "" {
			line += "  " + descStyle.Render(truncate(desc, 26))
		}
		rows = append(rows, line)
	}
	if r.err != "" {
		rows = append(rows, "", s.Error.Render(truncate(r.err, width-4)))
	}
	if len(r.created) > 0 {
		warn := fmt.Sprintf("Already created before the failure: %s — confirming again will duplicate them.", strings.Join(r.created, ", "))
		rows = append(rows, "", s.Warning.Width(width-4).Render(warn))
	}
	return strings.Join(rows, "\n")
}

// sliceRowDesc is a row's state column: dropped wins, otherwise its dependencies.
func sliceRowDesc(row sliceRow) string {
	if row.dropped {
		return "✗ dropped"
	}
	if len(row.slice.After) > 0 {
		return "after: " + strings.Join(row.slice.After, ", ")
	}
	return ""
}
