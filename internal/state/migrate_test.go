package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateLegacyRunsDirMovesAndPreservesState(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "runs")
	dst := filepath.Join(base, ".trau", "runs")
	if err := os.MkdirAll(filepath.Join(src, "COD-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "COD-1", "state"), []byte("PHASE=built\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	moved, err := MigrateLegacyRunsDir(src, dst)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !moved {
		t.Fatal("expected moved=true")
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("legacy dir should be gone, stat err = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "COD-1", "state"))
	if err != nil {
		t.Fatalf("read migrated state: %v", err)
	}
	if string(got) != "PHASE=built\n" {
		t.Errorf("state not preserved across move: %q", got)
	}
}

func TestMigrateLegacyRunsDirIdempotent(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "runs")
	dst := filepath.Join(base, ".trau", "runs")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if moved, err := MigrateLegacyRunsDir(src, dst); err != nil || !moved {
		t.Fatalf("first call: moved=%v err=%v", moved, err)
	}
	if moved, err := MigrateLegacyRunsDir(src, dst); err != nil || moved {
		t.Fatalf("second call should be a no-op: moved=%v err=%v", moved, err)
	}
}

func TestMigrateLegacyRunsDirSkipsWhenNewExists(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "runs")
	dst := filepath.Join(base, ".trau", "runs")
	if err := os.MkdirAll(filepath.Join(src, "COD-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "COD-1", "state"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "keep"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	moved, err := MigrateLegacyRunsDir(src, dst)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if moved {
		t.Error("must not move onto an existing new dir")
	}
	if _, err := os.Stat(filepath.Join(src, "COD-1", "state")); err != nil {
		t.Errorf("legacy dir was clobbered: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "keep")); err != nil {
		t.Errorf("new dir was clobbered: %v", err)
	}
}

func TestMigrateLegacyRunsDirNoLegacy(t *testing.T) {
	base := t.TempDir()
	moved, err := MigrateLegacyRunsDir(filepath.Join(base, "runs"), filepath.Join(base, ".trau", "runs"))
	if err != nil || moved {
		t.Fatalf("absent legacy dir should be a no-op: moved=%v err=%v", moved, err)
	}
}

func TestMigrateLegacyRunsDirSamePathNoop(t *testing.T) {
	base := t.TempDir()
	d := filepath.Join(base, "runs")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	moved, err := MigrateLegacyRunsDir(d, d)
	if err != nil || moved {
		t.Fatalf("identical paths should be a no-op: moved=%v err=%v", moved, err)
	}
}

func TestMigrateLegacyRunsDirIgnoresNonDir(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "runs")
	if err := os.WriteFile(src, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	moved, err := MigrateLegacyRunsDir(src, filepath.Join(base, ".trau", "runs"))
	if err != nil || moved {
		t.Fatalf("a file named runs should be a no-op: moved=%v err=%v", moved, err)
	}
}
