package webserver

import (
	"reflect"
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
