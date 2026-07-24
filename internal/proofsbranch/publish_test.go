package proofsbranch

import "testing"

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
