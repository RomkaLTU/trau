package browser

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestIsWSL(t *testing.T) {
	const wsl2 = "Linux version 5.15.153.1-microsoft-standard-WSL2 (root@builder) #1 SMP"
	const wsl1 = "Linux version 4.4.0-19041-Microsoft (Microsoft@Microsoft.com) #1237-Microsoft"
	const native = "Linux version 6.8.0-45-generic (buildd@lcy02) #45-Ubuntu SMP"

	tests := []struct {
		name    string
		distro  string
		version string
		want    bool
	}{
		{name: "distro env set", distro: "Ubuntu-24.04", version: native, want: true},
		{name: "wsl2 kernel", version: wsl2, want: true},
		{name: "wsl1 kernel mixed case", version: wsl1, want: true},
		{name: "native linux kernel", version: native, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "version")
			if err := os.WriteFile(path, []byte(tt.version+"\n"), 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			if got := isWSL(tt.distro, path); got != tt.want {
				t.Errorf("isWSL() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsWSLWithoutProcfs(t *testing.T) {
	if isWSL("", filepath.Join(t.TempDir(), "absent")) {
		t.Error("isWSL() = true with no env variable and no kernel string")
	}
}

func TestIsWSLReadsDistroEnv(t *testing.T) {
	t.Setenv("WSL_DISTRO_NAME", "Ubuntu-24.04")
	if !IsWSL() {
		t.Error("IsWSL() = false with WSL_DISTRO_NAME set")
	}
}

func TestOpeners(t *testing.T) {
	tests := []struct {
		name       string
		goos       string
		wsl        bool
		browserEnv string
		want       [][]string
	}{
		{
			name: "darwin",
			goos: "darwin",
			want: [][]string{{"open", "http://h/"}},
		},
		{
			name: "windows",
			goos: "windows",
			want: [][]string{{"rundll32", "url.dll,FileProtocolHandler", "http://h/"}},
		},
		{
			name: "linux",
			goos: "linux",
			want: [][]string{{"xdg-open", "http://h/"}},
		},
		{
			name: "linux ignores BROWSER outside wsl",
			goos: "linux", browserEnv: "firefox",
			want: [][]string{{"xdg-open", "http://h/"}},
		},
		{
			name: "wsl falls back to wslview",
			goos: "linux", wsl: true,
			want: [][]string{{"xdg-open", "http://h/"}, {"wslview", "http://h/"}},
		},
		{
			name: "wsl prefers BROWSER",
			goos: "linux", wsl: true, browserEnv: "/mnt/c/Program Files/edge.exe",
			want: [][]string{
				{"/mnt/c/Program Files/edge.exe", "http://h/"},
				{"xdg-open", "http://h/"},
				{"wslview", "http://h/"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := openers(tt.goos, tt.wsl, tt.browserEnv, "http://h/")
			if !slices.EqualFunc(got, tt.want, slices.Equal) {
				t.Errorf("openers() = %v, want %v", got, tt.want)
			}
		})
	}
}
