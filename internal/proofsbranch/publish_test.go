package proofsbranch

import (
	"slices"
	"testing"
)

func TestBuildPlanBootstrapsWhenBranchMissing(t *testing.T) {
	proofs := []Proof{
		{Seq: 1, Mime: "image/png"},
		{Seq: 2, Mime: "image/jpeg", Caption: "cart totals"},
	}
	pl := buildPlan("COD-1148", false, proofs)

	if !pl.Bootstrap {
		t.Error("a missing branch must be bootstrapped")
	}
	want := []File{
		{Path: "COD-1148/proof-1.png", Caption: "COD-1148 proof-1.png"},
		{Path: "COD-1148/proof-2.jpg", Caption: "cart totals"},
	}
	if len(pl.Files) != len(want) {
		t.Fatalf("planned %d files, want %d", len(pl.Files), len(want))
	}
	for i, f := range pl.Files {
		if f != want[i] {
			t.Errorf("file %d = %+v, want %+v", i, f, want[i])
		}
	}
}

func TestBuildPlanSkipsBootstrapWhenBranchExists(t *testing.T) {
	pl := buildPlan("COD-1148", true, []Proof{{Seq: 1, Mime: "image/png"}})

	if pl.Bootstrap {
		t.Error("an existing branch must not be bootstrapped")
	}
	if len(pl.Files) != 1 || pl.Files[0].Path != "COD-1148/proof-1.png" {
		t.Errorf("proofs still land under <ticket>/, got %+v", pl.Files)
	}
}

func TestSelectTreeEntriesDropsOnlyExpiredDirs(t *testing.T) {
	lsTree := "100644 blob aaaa\tREADME.md\n" +
		"040000 tree bbbb\tCOD-1\n" +
		"040000 tree cccc\tCOD-2\n" +
		"040000 tree dddd\tCOD-3\n"

	keep, dropped := selectTreeEntries(parseTreeEntries(lsTree), []string{"COD-2", "COD-4"})

	if !slices.Equal(dropped, []string{"COD-2"}) {
		t.Errorf("dropped = %v, want only the expired dir present on the branch (COD-2)", dropped)
	}
	wantKeep := []string{
		"100644 blob aaaa\tREADME.md",
		"040000 tree bbbb\tCOD-1",
		"040000 tree dddd\tCOD-3",
	}
	if !slices.Equal(keep, wantKeep) {
		t.Errorf("keep = %v, want the README and surviving dirs with their lines intact %v", keep, wantKeep)
	}
}

func TestSelectTreeEntriesKeepsAllWhenNoneExpired(t *testing.T) {
	entries := parseTreeEntries("040000 tree bbbb\tCOD-1\n040000 tree cccc\tCOD-2\n")
	keep, dropped := selectTreeEntries(entries, []string{"COD-9"})
	if len(dropped) != 0 {
		t.Errorf("dropped = %v, want nothing dropped when no expired dir is on the branch", dropped)
	}
	if len(keep) != 2 {
		t.Errorf("keep has %d entries, want both preserved", len(keep))
	}
}

func TestParseTreeEntriesSkipsMalformed(t *testing.T) {
	entries := parseTreeEntries("040000 tree bbbb\tCOD-1\n\nnot-a-tree-line\n")
	if len(entries) != 1 || entries[0].Name != "COD-1" {
		t.Fatalf("parseTreeEntries = %+v, want only the well-formed COD-1 entry", entries)
	}
}

func TestFilename(t *testing.T) {
	cases := []struct {
		seq  int
		mime string
		want string
	}{
		{1, "image/png", "proof-1.png"},
		{2, "image/jpeg", "proof-2.jpg"},
		{3, "image/gif", "proof-3.gif"},
		{4, "image/webp", "proof-4.webp"},
		{5, "IMAGE/PNG", "proof-5.png"},
		{6, "application/octet-stream", "proof-6"},
		{7, "", "proof-7"},
	}
	for _, tc := range cases {
		if got := filename(tc.seq, tc.mime); got != tc.want {
			t.Errorf("filename(%d, %q) = %q, want %q", tc.seq, tc.mime, got, tc.want)
		}
	}
}
