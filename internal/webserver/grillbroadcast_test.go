package webserver

import (
	"reflect"
	"slices"
	"testing"
)

// drainGrillFrames collects the numbered frames waiting on ch, dropping the rest.
func drainGrillFrames(ch <-chan liveGrillEvent) ([]GrillDeltaView, []GrillActivityView) {
	deltas, activity := []GrillDeltaView{}, []GrillActivityView{}
	for {
		select {
		case ev := <-ch:
			switch payload := ev.Payload.(type) {
			case GrillDeltaView:
				deltas = append(deltas, payload)
			case GrillActivityView:
				activity = append(activity, payload)
			}
		default:
			return deltas, activity
		}
	}
}

func drainGrillDeltas(ch <-chan liveGrillEvent) []GrillDeltaView {
	deltas, _ := drainGrillFrames(ch)
	return deltas
}

func grillDeltaEvent(sid int64, text string) liveGrillEvent {
	return liveGrillEvent{SessionID: sid, Event: "delta", Payload: GrillDeltaView{Text: text}}
}

func grillActivityFrame(sid int64, kind string) liveGrillEvent {
	return liveGrillEvent{SessionID: sid, Event: "activity", Payload: GrillActivityView{Kind: kind}}
}

// TestGrillBroadcasterRestartsSeqPerTurn covers the seam the hub and the panel meet
// at. One child serves every turn of an interview — it blocks inside ask_user rather
// than exiting — so the deltas after a question and its answer must still restart at
// one, since the panel clears its buffer on those frames and holes a reply whose seq
// skips.
func TestGrillBroadcasterRestartsSeqPerTurn(t *testing.T) {
	b := newGrillBroadcaster()
	sub, ch := b.subscribe()
	defer b.unsubscribe(sub)

	b.publish(grillDeltaEvent(7, "Let me "))
	b.publish(grillDeltaEvent(7, "push back."))
	b.publish(liveGrillEvent{SessionID: 7, Event: "message"})
	b.publish(liveGrillEvent{SessionID: 7, Event: "state"})
	b.publish(grillDeltaEvent(7, "And another "))
	b.publish(grillDeltaEvent(7, "thing."))

	want := []GrillDeltaView{
		{Seq: 1, Text: "Let me "},
		{Seq: 2, Text: "push back."},
		{Seq: 1, Text: "And another "},
		{Seq: 2, Text: "thing."},
	}
	if got := drainGrillDeltas(ch); !reflect.DeepEqual(got, want) {
		t.Errorf("deltas = %+v, want %+v", got, want)
	}
}

// TestGrillBroadcasterReplaysRecentActivity covers what a stream opening mid-turn is
// handed: the frames the turn has already reported, in the order it reported them and
// carrying the seq it numbered them with, so the panel reads them as one run.
func TestGrillBroadcasterReplaysRecentActivity(t *testing.T) {
	b := newGrillBroadcaster()

	b.publish(grillActivityFrame(7, "thinking"))
	b.publish(grillDeltaEvent(7, "Let me "))
	b.publish(grillActivityFrame(7, "tool"))
	b.publish(grillActivityFrame(9, "tool"))

	sub, _, replay := b.subscribeSession(7)
	defer b.unsubscribe(sub)

	want := []GrillActivityView{{Seq: 1, Kind: "thinking"}, {Seq: 2, Kind: "tool"}}
	if !reflect.DeepEqual(replay, want) {
		t.Errorf("replay = %+v, want %+v — the session's own frames, in order", replay, want)
	}
}

// TestGrillBroadcasterReplayEndsWithTheTurn pins the buffer to the same boundary the
// counters and the panel's ring clear on: the message or state frame that ends a turn
// leaves a stream opening afterwards nothing to replay.
func TestGrillBroadcasterReplayEndsWithTheTurn(t *testing.T) {
	for _, ending := range []string{"message", "state"} {
		t.Run(ending, func(t *testing.T) {
			b := newGrillBroadcaster()
			b.publish(grillActivityFrame(7, "thinking"))
			b.publish(grillActivityFrame(7, "tool"))
			b.publish(liveGrillEvent{SessionID: 7, Event: ending})

			sub, _, replay := b.subscribeSession(7)
			defer b.unsubscribe(sub)
			if len(replay) != 0 {
				t.Errorf("replay after %s = %+v, want nothing", ending, replay)
			}

			b.publish(grillActivityFrame(7, "tool"))
			sub2, _, next := b.subscribeSession(7)
			defer b.unsubscribe(sub2)
			want := []GrillActivityView{{Seq: 1, Kind: "tool"}}
			if !reflect.DeepEqual(next, want) {
				t.Errorf("replay of the next turn = %+v, want %+v", next, want)
			}
		})
	}
}

// TestGrillBroadcasterReplayIsBounded holds the buffer to the panel's own ring: a
// tool-heavy turn replays its most recent frames rather than all of them.
func TestGrillBroadcasterReplayIsBounded(t *testing.T) {
	b := newGrillBroadcaster()
	for i := 0; i < grillActivityReplay+10; i++ {
		b.publish(grillActivityFrame(7, "tool"))
	}

	sub, _, replay := b.subscribeSession(7)
	defer b.unsubscribe(sub)

	if len(replay) != grillActivityReplay {
		t.Fatalf("replay length = %d, want %d", len(replay), grillActivityReplay)
	}
	if replay[0].Seq != 11 || replay[len(replay)-1].Seq != grillActivityReplay+10 {
		t.Errorf("replay spans seq %d..%d, want 11..%d", replay[0].Seq,
			replay[len(replay)-1].Seq, grillActivityReplay+10)
	}
}

// TestGrillBroadcasterReplayReachesLiveFramesOnce pins the seam a stream opens on: a
// frame already buffered when the subscriber joins arrives in the replay, and every
// frame after it on the channel, so neither is delivered twice.
func TestGrillBroadcasterReplayReachesLiveFramesOnce(t *testing.T) {
	b := newGrillBroadcaster()
	b.publish(grillActivityFrame(7, "thinking"))

	sub, ch, replay := b.subscribeSession(7)
	defer b.unsubscribe(sub)
	b.publish(grillActivityFrame(7, "tool"))

	_, live := drainGrillFrames(ch)
	got := append(slices.Clone(replay), live...)
	want := []GrillActivityView{{Seq: 1, Kind: "thinking"}, {Seq: 2, Kind: "tool"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("replay then live = %+v, want %+v", got, want)
	}
}

// TestGrillBroadcasterRemoveSessionDropsReplay covers the session that is gone for
// good: nothing it left behind outlives it.
func TestGrillBroadcasterRemoveSessionDropsReplay(t *testing.T) {
	b := newGrillBroadcaster()
	b.publish(grillActivityFrame(7, "tool"))
	b.publish(grillDeltaEvent(7, "Let me "))
	b.removeSession(7)

	sub, _, replay := b.subscribeSession(7)
	defer b.unsubscribe(sub)
	if len(replay) != 0 {
		t.Errorf("replay after remove = %+v, want nothing", replay)
	}
	if len(b.recent) != 0 || len(b.seq) != 0 || len(b.activity) != 0 {
		t.Errorf("broadcaster still holds %d rings, %d delta counts, %d activity counts",
			len(b.recent), len(b.seq), len(b.activity))
	}
}

// TestGrillBroadcasterNumbersActivityApartFromDeltas pins the seam a tool-heavy turn
// opens: activity lands between the reply's chunks, so sharing the delta count would
// leave every reply holed over a gap the panel reads as a dropped chunk. Both counts
// still restart together on the frame that ends the turn.
func TestGrillBroadcasterNumbersActivityApartFromDeltas(t *testing.T) {
	b := newGrillBroadcaster()
	sub, ch := b.subscribe()
	defer b.unsubscribe(sub)

	b.publish(grillActivityFrame(7, "thinking"))
	b.publish(grillDeltaEvent(7, "Let me "))
	b.publish(grillActivityFrame(7, "tool"))
	b.publish(grillDeltaEvent(7, "push back."))
	b.publish(liveGrillEvent{SessionID: 7, Event: "message"})
	b.publish(grillActivityFrame(7, "thinking"))
	b.publish(grillDeltaEvent(7, "And another thing."))

	wantDeltas := []GrillDeltaView{
		{Seq: 1, Text: "Let me "},
		{Seq: 2, Text: "push back."},
		{Seq: 1, Text: "And another thing."},
	}
	wantActivity := []GrillActivityView{
		{Seq: 1, Kind: "thinking"},
		{Seq: 2, Kind: "tool"},
		{Seq: 1, Kind: "thinking"},
	}
	gotDeltas, gotActivity := drainGrillFrames(ch)
	if !reflect.DeepEqual(gotDeltas, wantDeltas) {
		t.Errorf("deltas = %+v, want %+v", gotDeltas, wantDeltas)
	}
	if !reflect.DeepEqual(gotActivity, wantActivity) {
		t.Errorf("activity = %+v, want %+v", gotActivity, wantActivity)
	}
}

// TestGrillBroadcasterNumbersSessionsApart pins the count to the session: one
// broadcaster feeds every stream, so interleaved sessions must not share a turn.
func TestGrillBroadcasterNumbersSessionsApart(t *testing.T) {
	b := newGrillBroadcaster()
	sub, ch := b.subscribe()
	defer b.unsubscribe(sub)

	b.publish(grillDeltaEvent(1, "one "))
	b.publish(grillDeltaEvent(2, "two "))
	b.publish(liveGrillEvent{SessionID: 2, Event: "state"})
	b.publish(grillDeltaEvent(1, "more."))

	want := []GrillDeltaView{
		{Seq: 1, Text: "one "},
		{Seq: 1, Text: "two "},
		{Seq: 2, Text: "more."},
	}
	if got := drainGrillDeltas(ch); !reflect.DeepEqual(got, want) {
		t.Errorf("deltas = %+v, want %+v", got, want)
	}
}
