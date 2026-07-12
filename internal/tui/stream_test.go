package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/RomkaLTU/trau/internal/event"
)

type fakeTranscriptSource struct {
	delta    TranscriptDelta
	err      error
	gotID    string
	gotAfter int64
}

func (f *fakeTranscriptSource) Chunks(_ context.Context, _, id string, after int64) (TranscriptDelta, error) {
	f.gotID, f.gotAfter = id, after
	return f.delta, f.err
}

// TestPollTranscriptFromSource checks the live view polls the wired hub source with
// the current session/cursor and wraps its delta as the emulator message.
func TestPollTranscriptFromSource(t *testing.T) {
	src := &fakeTranscriptSource{delta: TranscriptDelta{ID: "1-build", Cols: 80, Rows: 24, Data: []byte("hi"), Seq: 3}}
	SetTranscriptSource(src, "acme")
	t.Cleanup(func() { SetTranscriptSource(nil, "") })

	msg, ok := pollTranscript("1-build", 2)().(streamDataMsg)
	if !ok {
		t.Fatal("poll must produce a streamDataMsg")
	}
	if msg.id != "1-build" || string(msg.data) != "hi" || msg.seq != 3 {
		t.Fatalf("delta = %+v, want the source's chunk", msg)
	}
	if src.gotID != "1-build" || src.gotAfter != 2 {
		t.Fatalf("source polled with id=%q after=%d, want 1-build/2", src.gotID, src.gotAfter)
	}
}

// TestPollTranscriptNoSource checks the empty-source guard idles at the cursor
// instead of erroring, so a run without a hub leaves the pane blank.
func TestPollTranscriptNoSource(t *testing.T) {
	SetTranscriptSource(nil, "")
	msg := pollTranscript("1-build", 5)().(streamDataMsg)
	if len(msg.data) != 0 || msg.seq != 5 {
		t.Fatalf("no source: got data=%q seq=%d, want empty at seq 5", msg.data, msg.seq)
	}
}

// TestStreamDataAppends checks a matching-id delta lands in the emulator and
// advances the cursor, while a stale-id delta is dropped.
func TestStreamDataAppends(t *testing.T) {
	m := initialModel(nil)
	m.streamID = "1-build"
	m.streamCols, m.streamRows = 80, 24
	m.startStream()

	nm, _ := m.Update(streamDataMsg{id: "1-build", seq: 4, data: []byte("AAAAA")})
	m = nm.(model)
	if !strings.Contains(strings.Join(m.stream.Lines(), ""), "AAAAA") {
		t.Fatal("matching-id delta must land in the emulator")
	}
	if m.streamSeq != 4 {
		t.Errorf("streamSeq = %d, want 4 from the delta", m.streamSeq)
	}

	nm, _ = m.Update(streamDataMsg{id: "2-verify", seq: 1, data: []byte("BBB")})
	m = nm.(model)
	if strings.Contains(strings.Join(m.stream.Lines(), ""), "BBB") {
		t.Error("a delta for a stale session id must be dropped")
	}
}

// TestApplyEventAgentStartTracksID checks agent_start records the active session id
// and re-points (resetting the emulator) when a new phase starts.
func TestApplyEventAgentStartTracksID(t *testing.T) {
	m := initialModel(nil)
	m.applyEvent(event.Event{Kind: event.KindAgentStart, Fields: map[string]any{"transcript_id": "1-build", "cols": 80, "rows": 24}})
	if m.streamID != "1-build" {
		t.Fatalf("streamID = %q, want the build session", m.streamID)
	}
	m.applyEvent(event.Event{Kind: event.KindAgentStart, Fields: map[string]any{"transcript_id": "2-verify"}})
	if m.streamID != "2-verify" {
		t.Errorf("new phase must re-point, got %q", m.streamID)
	}
}

// TestWatchKeyTogglesStream checks w expands the full live screen when a session is
// known and collapses it on the second press, keeping the emulator alive.
func TestWatchKeyTogglesStream(t *testing.T) {
	m := initialModel(nil)
	m.streamID = "1-build"
	w := tea.KeyPressMsg{Code: 'w', Text: "w"}

	m, _, handled := m.handleKey(w)
	if !handled || !m.streaming || m.stream == nil {
		t.Fatalf("first w must expand the live screen (handled=%v streaming=%v stream=%v)", handled, m.streaming, m.stream != nil)
	}
	m, _, handled = m.handleKey(w)
	if !handled || m.streaming {
		t.Fatalf("second w must collapse the live view (handled=%v streaming=%v)", handled, m.streaming)
	}
	if m.stream == nil {
		t.Fatal("collapsing the w view must keep the tail emulator alive for the span pane")
	}
}

// TestRenderStreamPlaceholder checks the pane shows the live-view placeholder when
// no live screen is active.
func TestRenderStreamPlaceholder(t *testing.T) {
	m := initialModel(nil)
	if out := m.renderStream(m.dims()); !strings.Contains(out, "live agent view") {
		t.Errorf("no-stream pane must show the placeholder, got:\n%s", out)
	}
}
