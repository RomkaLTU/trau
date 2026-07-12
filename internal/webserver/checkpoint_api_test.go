package webserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func doReq(t *testing.T, method, url string, body any) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return res
}

// TestCheckpointAPIRoundTrip exercises the loop child's checkpoint seam end to
// end over HTTP (ADR 0008): a write lands in the authoritative table, reads and
// the whole-repo list serve it back, and a delete removes it — the child never
// opens the database.
func TestCheckpointAPIRoundTrip(t *testing.T) {
	home := t.TempDir()
	checkpointRepo(t, home, "acme")
	_, ts := controlServer(t, home, nil)
	base := ts.URL + APIPrefix + "/repos/acme"

	put := doReq(t, http.MethodPut, base+"/runs/COD-1/checkpoint", checkpointPut{
		Data: map[string]string{"PHASE": "built", "BRANCH": "feature/COD-1", "TITLE": "Do it"},
	})
	if put.StatusCode != http.StatusOK {
		t.Fatalf("PUT checkpoint = %d, want 200", put.StatusCode)
	}
	_ = put.Body.Close()

	get := doReq(t, http.MethodGet, base+"/runs/COD-1/checkpoint", nil)
	if get.StatusCode != http.StatusOK {
		t.Fatalf("GET checkpoint = %d, want 200", get.StatusCode)
	}
	var view struct {
		Phase string            `json:"phase"`
		Data  map[string]string `json:"data"`
	}
	if err := json.NewDecoder(get.Body).Decode(&view); err != nil {
		t.Fatalf("decode checkpoint: %v", err)
	}
	_ = get.Body.Close()
	if view.Phase != "built" || view.Data["BRANCH"] != "feature/COD-1" {
		t.Fatalf("checkpoint = %+v; want phase built, branch feature/COD-1", view)
	}

	list := doReq(t, http.MethodGet, base+"/checkpoints", nil)
	var listed struct {
		Checkpoints []struct {
			Ticket string `json:"ticket"`
			Phase  string `json:"phase"`
		} `json:"checkpoints"`
	}
	if err := json.NewDecoder(list.Body).Decode(&listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	_ = list.Body.Close()
	if len(listed.Checkpoints) != 1 || listed.Checkpoints[0].Ticket != "COD-1" {
		t.Fatalf("list = %+v; want one COD-1", listed.Checkpoints)
	}

	del := doReq(t, http.MethodDelete, base+"/runs/COD-1/checkpoint", nil)
	if del.StatusCode != http.StatusOK {
		t.Fatalf("DELETE checkpoint = %d, want 200", del.StatusCode)
	}
	_ = del.Body.Close()

	gone := doReq(t, http.MethodGet, base+"/runs/COD-1/checkpoint", nil)
	if gone.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after delete = %d, want 404", gone.StatusCode)
	}
	_ = gone.Body.Close()
}
