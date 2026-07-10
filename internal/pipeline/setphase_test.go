package pipeline

import (
	"reflect"
	"testing"

	"github.com/RomkaLTU/trau/internal/state"
)

func TestSetPhaseWritesCheckpointAndReports(t *testing.T) {
	p := &Pipeline{State: state.NewStore(t.TempDir())}
	var got []string
	p.OnPhase = func(id, phase string) { got = append(got, id+"/"+phase) }

	for _, ph := range []string{state.Building, state.Built, state.HandedOff} {
		if err := p.setPhase("COD-1", ph); err != nil {
			t.Fatalf("setPhase(%s): %v", ph, err)
		}
	}

	if got := p.State.Get("COD-1", "PHASE"); got != state.HandedOff {
		t.Fatalf("checkpoint PHASE = %q, want %q", got, state.HandedOff)
	}
	want := []string{"COD-1/building", "COD-1/built", "COD-1/handed_off"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OnPhase calls = %v, want %v", got, want)
	}
}

func TestSetPhaseCheckpointsWithoutReporter(t *testing.T) {
	p := &Pipeline{State: state.NewStore(t.TempDir())}
	if err := p.setPhase("COD-1", state.Verified); err != nil {
		t.Fatalf("setPhase: %v", err)
	}
	if got := p.State.Get("COD-1", "PHASE"); got != state.Verified {
		t.Fatalf("checkpoint PHASE = %q, want %q", got, state.Verified)
	}
}
