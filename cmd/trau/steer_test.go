package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/webserver"
)

func TestParseSteerArgs(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		stdin      string
		wantTicket string
		wantBody   string
		wantRepo   string
		wantErr    bool
	}{
		{
			name:       "single quoted note",
			args:       []string{"COD-1088", "use the REST adapter"},
			wantTicket: "COD-1088",
			wantBody:   "use the REST adapter",
		},
		{
			name:       "trailing words join with spaces",
			args:       []string{"COD-1088", "use", "the", "REST", "adapter"},
			wantTicket: "COD-1088",
			wantBody:   "use the REST adapter",
		},
		{
			name:       "stdin keeps the note's line breaks",
			args:       []string{"COD-1088", "-"},
			stdin:      "first line\nsecond line\n",
			wantTicket: "COD-1088",
			wantBody:   "first line\nsecond line",
		},
		{
			name:       "repo flag after the note",
			args:       []string{"COD-1088", "use", "the", "REST", "adapter", "--repo", "/src/acme"},
			wantTicket: "COD-1088",
			wantBody:   "use the REST adapter",
			wantRepo:   "/src/acme",
		},
		{
			name:    "no note",
			args:    []string{"COD-1088"},
			wantErr: true,
		},
		{
			name:    "whitespace-only note",
			args:    []string{"COD-1088", "   "},
			wantErr: true,
		},
		{
			name:    "empty stdin",
			args:    []string{"COD-1088", "-"},
			stdin:   "\n\n",
			wantErr: true,
		},
		{
			name:    "no ticket",
			args:    []string{"-"},
			stdin:   "steer the agent",
			wantErr: true,
		},
		{
			name:    "unknown flag",
			args:    []string{"COD-1088", "--force", "note"},
			wantErr: true,
		},
		{
			name:    "note word that reads as a flag",
			args:    []string{"COD-1088", "add", "-v", "now"},
			wantErr: true,
		},
		{
			name:    "repo flag without a value",
			args:    []string{"COD-1088", "note", "--repo"},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSteerArgs(tc.args, strings.NewReader(tc.stdin))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseSteerArgs(%q) = %+v, want an error", tc.args, got)
				}
				var ue usageError
				if !errors.As(err, &ue) {
					t.Errorf("error %v is not a usage error, so trau would not exit 2", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSteerArgs(%q) = %v", tc.args, err)
			}
			if got.ticket != tc.wantTicket {
				t.Errorf("ticket = %q, want %q", got.ticket, tc.wantTicket)
			}
			if got.body != tc.wantBody {
				t.Errorf("body = %q, want %q", got.body, tc.wantBody)
			}
			if got.repo != tc.wantRepo {
				t.Errorf("repo = %q, want %q", got.repo, tc.wantRepo)
			}
		})
	}
}

func TestQueueSteerNote(t *testing.T) {
	var got struct {
		Ticket string `json:"ticket"`
		Body   string `json:"body"`
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/repos/acme/steer" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode queue body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":12,"ticket":"COD-1088","body":"use the REST adapter","status":"pending"}`)
	}))
	defer ts.Close()

	var out bytes.Buffer
	err := queueSteerNote(context.Background(), hubclient.New(ts.URL, ""), "acme", "COD-1088", "use the REST adapter", &out)
	if err != nil {
		t.Fatalf("queueSteerNote = %v", err)
	}
	if got.Ticket != "COD-1088" || got.Body != "use the REST adapter" {
		t.Errorf("hub received %+v, want the ticket and note as typed", got)
	}
	line := out.String()
	if strings.Count(line, "\n") != 1 {
		t.Errorf("stdout = %q, want exactly one line", line)
	}
	if !strings.Contains(line, "12") || !strings.Contains(line, "COD-1088") || !strings.Contains(line, "queued") {
		t.Errorf("stdout = %q, want the note id, ticket, and its state", line)
	}
}

// TestRunDispatchesSteerBeforeGlobalFlags checks a note word that reads as one of
// trau's global flags reaches steer rather than printing the version or the help
// and exiting 0 with the note silently dropped.
func TestRunDispatchesSteerBeforeGlobalFlags(t *testing.T) {
	for _, word := range []string{"-v", "--version", "-h", "--help"} {
		t.Run(word, func(t *testing.T) {
			var out bytes.Buffer
			err := run(context.Background(), []string{"steer", "COD-1088", "add", word, "now"}, &out, io.Discard)
			var ue usageError
			if !errors.As(err, &ue) {
				t.Fatalf("run(steer … %s …) = %v, want a usage error", word, err)
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want nothing — a global flag claimed the note word", out.String())
			}
		})
	}
}

func TestRunSteerHelp(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"steer", "--help"}, &out, io.Discard); err != nil {
		t.Fatalf("run(steer --help) = %v", err)
	}
	if !strings.Contains(out.String(), "trau steer <ID> <note>") {
		t.Errorf("stdout = %q, want steer's own usage", out.String())
	}
}

// TestRunSteerRepoFlagBeatsEnv checks the ADR 0001 §2 order holds for steer:
// --repo outranks TRAU_REPO_ROOT, so a note never lands on another repo while the
// operator is told it was queued.
func TestRunSteerRepoFlagBeatsEnv(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == webserver.APIPrefix+"/health" {
			_ = json.NewEncoder(w).Encode(webserver.Health{Status: "ok", Version: version})
			return
		}
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":1,"ticket":"PREC-1","body":"note","status":"pending"}`)
	}))
	defer ts.Close()

	host, port, err := net.SplitHostPort(ts.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("TRAU_REPO_ROOT", filepath.Join(root, "beta"))
	t.Setenv("SERVE_BIND", host)
	t.Setenv("SERVE_PORT", port)

	var out bytes.Buffer
	if err := runSteer(context.Background(), []string{"PREC-1", "note", "--repo", filepath.Join(root, "acme")}, &out, io.Discard); err != nil {
		t.Fatalf("runSteer = %v", err)
	}
	if want := webserver.APIPrefix + "/repos/acme/steer"; gotPath != want {
		t.Errorf("hub received %q, want %q — --repo lost to TRAU_REPO_ROOT", gotPath, want)
	}
}

func TestQueueSteerNoteUnknownRepo(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer ts.Close()

	var out bytes.Buffer
	err := queueSteerNote(context.Background(), hubclient.New(ts.URL, ""), "acme", "COD-1088", "note", &out)
	if err == nil || !strings.Contains(console.FormatActionable(err), "acme") {
		t.Fatalf("err = %v, want a refusal naming the repo the hub does not know", err)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want nothing written on a failed queue", out.String())
	}
}
