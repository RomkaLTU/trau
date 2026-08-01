package webserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/registry"
)

// TestQueueStartRefusesAFolderRepoWhileAChildIsLive is the collision guard at the
// hub: a Folder repo whose child already has a loop in it cannot arm its queue,
// and the 409 names both the repo holding the working tree and the ticket its loop
// is on.
func TestQueueStartRefusesAFolderRepoWhileAChildIsLive(t *testing.T) {
	s, _, root := drainServer(t, "PortalPro")
	child := filepath.Join(root, "api-companies")
	if err := os.MkdirAll(filepath.Join(child, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeInstanceEntry(t, s, registry.Entry{
		PID:          os.Getpid(),
		RepoRoot:     child,
		SessionState: registry.StateWorking,
		Ticket:       "COD-77",
		Heartbeat:    time.Now(),
	})
	seedQueue(t, s, root, false, queue.Item{ID: "COD-9"})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/PortalPro/queue/drain", DrainRequest{Draining: true})
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("start = %d, want 409 (body %s)", res.StatusCode, body)
	}
	var refusal struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &refusal); err != nil {
		t.Fatalf("decode refusal %s: %v", body, err)
	}
	for _, want := range []string{"COD-77", child} {
		if !strings.Contains(refusal.Error, want) {
			t.Errorf("refusal %q, want it to name %q", refusal.Error, want)
		}
	}
	if drainingOf(t, s, root) {
		t.Error("the refused start armed the queue anyway")
	}
}
