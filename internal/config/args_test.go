package config

import "testing"

func TestParseArgsClear(t *testing.T) {
	o, err := ParseArgs([]string{"--clear", "COD-566"})
	if err != nil {
		t.Fatalf("ParseArgs(--clear COD-566): %v", err)
	}
	if o.ClearID != "COD-566" {
		t.Errorf("ClearID = %q, want COD-566", o.ClearID)
	}
}

func TestParseArgsForgetAlias(t *testing.T) {
	o, err := ParseArgs([]string{"--forget", "COD-7"})
	if err != nil {
		t.Fatalf("ParseArgs(--forget COD-7): %v", err)
	}
	if o.ClearID != "COD-7" {
		t.Errorf("ClearID = %q, want COD-7 (--forget should alias --clear)", o.ClearID)
	}
}

func TestParseArgsClearRequiresValue(t *testing.T) {
	if _, err := ParseArgs([]string{"--clear"}); err == nil {
		t.Error("ParseArgs(--clear) without a value should error")
	}
}

func TestParseArgsClearMutuallyExclusive(t *testing.T) {
	pairs := [][]string{
		{"--clear", "COD-1", "--status"},
		{"--clear", "COD-1", "--reset", "COD-2"},
		{"--clear", "COD-1", "--dry-run"},
		{"--clear", "COD-1", "--list-eligible"},
	}
	for _, args := range pairs {
		if _, err := ParseArgs(args); err == nil {
			t.Errorf("ParseArgs(%v) should reject combining --clear with another mode", args)
		}
	}
}

func TestParseArgsListEligible(t *testing.T) {
	o, err := ParseArgs([]string{"--list-eligible", "--json"})
	if err != nil {
		t.Fatalf("ParseArgs(--list-eligible --json): %v", err)
	}
	if !o.ListEligible {
		t.Error("ListEligible = false, want true")
	}
	if !o.JSON {
		t.Error("JSON = false, want true")
	}
}

func TestParseArgsListEligibleMutuallyExclusive(t *testing.T) {
	pairs := [][]string{
		{"--list-eligible", "--status"},
		{"--list-eligible", "--dry-run"},
		{"--list-eligible", "--reset", "COD-2"},
	}
	for _, args := range pairs {
		if _, err := ParseArgs(args); err == nil {
			t.Errorf("ParseArgs(%v) should reject combining --list-eligible with another mode", args)
		}
	}
}
