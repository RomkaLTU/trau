package webserver

import (
	"reflect"
	"testing"
)

// drainGrillDeltas collects the delta frames waiting on ch, dropping the rest.
func drainGrillDeltas(ch <-chan liveGrillEvent) []GrillDeltaView {
	got := []GrillDeltaView{}
	for {
		select {
		case ev := <-ch:
			if delta, ok := ev.Payload.(GrillDeltaView); ok {
				got = append(got, delta)
			}
		default:
			return got
		}
	}
}

func grillDeltaEvent(sid int64, text string) liveGrillEvent {
	return liveGrillEvent{SessionID: sid, Event: "delta", Payload: GrillDeltaView{Text: text}}
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
