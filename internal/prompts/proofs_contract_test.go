package prompts

import (
	"strings"
	"testing"
)

const goldenProofsDir = "/tmp/trau-proofs-COD-123"

func proofsVerifyData(note string, contract bool) VerifyData {
	return VerifyData{
		ID:             goldenID,
		Handoff:        goldenHandoffPath,
		Verdict:        goldenVerdictPath,
		Note:           note,
		ProofsContract: contract,
		ProofsDir:      goldenProofsDir,
	}
}

func TestVerifyProofsContractDoesNotDependOnAppURL(t *testing.T) {
	got := Render("verify", proofsVerifyData("", true))

	wanted := []string{
		"whether or not an app URL is named above",
		`start_recording("trau-COD-123", title="COD-123")`,
		"stop_recording()",
		goldenProofsDir + "/manifest.json",
		`"trace_dir"`,
		"Skip all of this only when you drive no browser.",
	}
	for _, want := range wanted {
		if !strings.Contains(got, want) {
			t.Errorf("verify prompt with no browser note is missing %q", want)
		}
	}
	if strings.Count(got, goldenProofsDir) < 2 {
		t.Errorf("verify prompt names the proofs dir %d time(s), want the screenshots and the manifest both anchored to it", strings.Count(got, goldenProofsDir))
	}
}

func TestVerifyProofsContractOmittedWhenDisabled(t *testing.T) {
	got := Render("verify", proofsVerifyData("NOTE.", false))

	for _, unwanted := range []string{"start_recording", "manifest.json", goldenProofsDir} {
		if strings.Contains(got, unwanted) {
			t.Errorf("verify prompt without the proofs contract still mentions %q", unwanted)
		}
	}
}
