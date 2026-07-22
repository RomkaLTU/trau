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

func TestParseArgsResetLocal(t *testing.T) {
	o, err := ParseArgs([]string{"--reset-local", "COD-1094"})
	if err != nil {
		t.Fatalf("ParseArgs(--reset-local COD-1094): %v", err)
	}
	if o.ResetLocalID != "COD-1094" {
		t.Errorf("ResetLocalID = %q, want COD-1094", o.ResetLocalID)
	}
	if o.ResetID != "" {
		t.Errorf("ResetID = %q, want empty", o.ResetID)
	}
}

func TestParseArgsResetLocalRequiresValue(t *testing.T) {
	if _, err := ParseArgs([]string{"--reset-local"}); err == nil {
		t.Error("ParseArgs(--reset-local) without a value should error")
	}
}

func TestParseArgsResetLocalMutuallyExclusive(t *testing.T) {
	pairs := [][]string{
		{"--reset-local", "COD-1", "--reset", "COD-2"},
		{"--reset-local", "COD-1", "--clear", "COD-2"},
		{"--reset-local", "COD-1", "--status"},
	}
	for _, args := range pairs {
		if _, err := ParseArgs(args); err == nil {
			t.Errorf("ParseArgs(%v) should reject combining --reset-local with another mode", args)
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

func TestParseArgsListEpic(t *testing.T) {
	o, err := ParseArgs([]string{"--list-epic", "COD-530", "--json"})
	if err != nil {
		t.Fatalf("ParseArgs(--list-epic COD-530 --json): %v", err)
	}
	if o.ListEpicID != "COD-530" {
		t.Errorf("ListEpicID = %q, want COD-530", o.ListEpicID)
	}
	if !o.JSON {
		t.Error("JSON = false, want true")
	}
}

func TestParseArgsListEpicRequiresValue(t *testing.T) {
	if _, err := ParseArgs([]string{"--list-epic"}); err == nil {
		t.Error("ParseArgs(--list-epic) without a value should error")
	}
}

func TestParseArgsListEpicMutuallyExclusive(t *testing.T) {
	pairs := [][]string{
		{"--list-epic", "COD-1", "--status"},
		{"--list-epic", "COD-1", "--dry-run"},
		{"--list-epic", "COD-1", "--list-eligible"},
		{"--list-epic", "COD-1", "--reset", "COD-2"},
	}
	for _, args := range pairs {
		if _, err := ParseArgs(args); err == nil {
			t.Errorf("ParseArgs(%v) should reject combining --list-epic with another mode", args)
		}
	}
}
