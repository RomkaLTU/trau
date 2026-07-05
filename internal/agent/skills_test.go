package agent

import (
	"reflect"
	"testing"
)

func TestSkillInstallArgs(t *testing.T) {
	cases := []struct {
		name string
		pkg  string
		want []string
	}{
		{
			name: "package with skill selector",
			pkg:  "samber/cc-skills-golang@golang-code-style",
			want: []string{"-y", "skills", "add", "samber/cc-skills-golang", "-s", "golang-code-style", "-y"},
		},
		{
			name: "bare repository package",
			pkg:  "vercel-labs/agent-skills",
			want: []string{"-y", "skills", "add", "vercel-labs/agent-skills", "-y"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := skillInstallArgs(SkillRecommendation{Package: tc.pkg})
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("skillInstallArgs(%q) = %v, want %v", tc.pkg, got, tc.want)
			}
		})
	}
}
