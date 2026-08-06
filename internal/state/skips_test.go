package state

import (
	"slices"
	"testing"
)

// The checkpoint is the only place a per-run skip set survives, so what goes in
// must come back out unchanged (ADR 0037).
func TestSkipsRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		keys    []string
		encoded string
	}{
		{name: "none", keys: nil, encoded: ""},
		{name: "one key", keys: []string{"verify"}, encoded: "verify"},
		{name: "several keys keep their order", keys: []string{"verify", "ci", "merge"}, encoded: "verify,ci,merge"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EncodeSkips(tc.keys); got != tc.encoded {
				t.Errorf("EncodeSkips(%v) = %q, want %q", tc.keys, got, tc.encoded)
			}
			if got := DecodeSkips(tc.encoded); !slices.Equal(got, tc.keys) {
				t.Errorf("DecodeSkips(%q) = %v, want %v", tc.encoded, got, tc.keys)
			}
		})
	}
}

// A checkpoint written by another binary must never make this one fail to resume,
// so decoding tolerates spacing and empty entries and validates nothing.
func TestDecodeSkipsTolerance(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{in: "", want: nil},
		{in: ",", want: nil},
		{in: " verify , ci ", want: []string{"verify", "ci"}},
		{in: "verify,,ci", want: []string{"verify", "ci"}},
		{in: "somethingnew", want: []string{"somethingnew"}},
	}
	for _, tc := range cases {
		if got := DecodeSkips(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("DecodeSkips(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// The set travels on the ticket's own state, under a key that must stay stable.
func TestStoreRoundTripsSkips(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Set("COD-1519", SkipsKey, EncodeSkips([]string{"verify", "ci"})); err != nil {
		t.Fatal(err)
	}
	if got := DecodeSkips(s.Get("COD-1519", SkipsKey)); !slices.Equal(got, []string{"verify", "ci"}) {
		t.Errorf("stored skips = %v, want [verify ci]", got)
	}
	if got := s.Get("COD-1520", SkipsKey); got != "" {
		t.Errorf("an untouched ticket reports skips %q, want none", got)
	}
}
