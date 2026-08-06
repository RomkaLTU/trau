package webserver

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/state"
)

// A run the operator delivered with work bypassed has to say so on the read side:
// the skip set rides the run row, and a run that skipped nothing carries no field
// at all rather than an empty one (ADR 0037).
func TestRunViewCarriesTheSkipSet(t *testing.T) {
	cases := []struct {
		name      string
		skips     []string
		want      []string
		wantField bool
	}{
		{name: "verify and ci skipped", skips: []string{"verify", "ci"}, want: []string{"verify", "ci"}, wantField: true},
		{name: "nothing skipped", skips: nil, want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := runViewFromCheckpoint(hubstore.TicketCheckpoint{
				Ticket: "COD-1519",
				CheckpointRow: hubstore.CheckpointRow{
					Phase: state.Merged,
					Skips: tc.skips,
				},
			})
			if !slices.Equal(run.Skips, tc.want) {
				t.Errorf("run.Skips = %v, want %v", run.Skips, tc.want)
			}
			blob, err := json.Marshal(RunDetail{RunView: run})
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(blob), `"skips"`); got != tc.wantField {
				t.Errorf("run detail JSON carries a skips field = %v, want %v (%s)", got, tc.wantField, blob)
			}
		})
	}
}
