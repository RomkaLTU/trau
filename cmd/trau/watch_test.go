package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubclient"
)

// TestWatcherPollsHub checks the watcher pulls the live transcript from the hub's
// chunk store, switches to the resolved session, and renders its bytes — the file
// tail is gone (ADR 0008 §4).
func TestWatcherPollsHub(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("hello agent"))
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/transcript/chunks") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"100-build","cols":80,"rows":24,"chunks":[{"seq":0,"data":%q}]}`, data)
	}))
	defer ts.Close()

	var out, status bytes.Buffer
	w := &watcher{out: &out, status: &status, hub: hubclient.New(ts.URL, ""), repo: "acme", follow: true, seq: -1}
	w.tick(context.Background())
	defer func() {
		if w.screen != nil {
			w.screen.Close()
		}
	}()

	if w.curID != "100-build" {
		t.Fatalf("curID = %q, want the resolved session", w.curID)
	}
	if w.screen == nil {
		t.Fatal("a resolved session must start the emulator")
	}
	if !strings.Contains(out.String(), "hello agent") {
		t.Errorf("snapshot missing agent output:\n%s", out.String())
	}
}

func TestTrimTrailingBlank(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want int
	}{
		{"trailing spaces and ansi", []string{"hi", "there", "   ", "\x1b[0m  \x1b[0m"}, 2},
		{"internal blanks kept", []string{"a", "", "b", ""}, 3},
		{"all blank", []string{"", "  ", "\x1b[0m"}, 0},
		{"none blank", []string{"a", "b"}, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := trimTrailingBlank(tc.in); len(got) != tc.want {
				t.Errorf("trimTrailingBlank(%q) len = %d, want %d", tc.in, len(got), tc.want)
			}
		})
	}
}
