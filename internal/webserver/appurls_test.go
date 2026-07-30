package webserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
)

func appURLServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	seedRepo(t, home, "acme")
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, ts.URL + APIPrefix + "/repos/acme/app-urls"
}

func createAppURL(t *testing.T, base string, body AppURLRequest) AppURLView {
	t.Helper()
	res := postJSON(t, base, body)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create app url status = %d, want 201", res.StatusCode)
	}
	var out AppURLView
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode created app url: %v", err)
	}
	return out
}

func listAppURLs(t *testing.T, base string) []AppURLView {
	t.Helper()
	res, err := http.Get(base)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	var out []AppURLView
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	return out
}

func TestAppURLCreateAndList(t *testing.T) {
	_, base := appURLServer(t)

	created := createAppURL(t, base, AppURLRequest{Label: "storefront", URL: " http://localhost:3000 "})
	if created.URL != "http://localhost:3000" {
		t.Errorf("created url = %q, want it trimmed", created.URL)
	}
	createAppURL(t, base, AppURLRequest{Label: "api", URL: "http://localhost:3001", Workspace: "api"})

	list := listAppURLs(t, base)
	if len(list) != 2 || list[0].Workspace != "" || list[1].Workspace != "api" {
		t.Fatalf("list = %+v, want the default entry then the api workspace", list)
	}
}

// TestAppURLRejectsMissingURL covers the one required field: the entry exists to
// name a target, so a blank url is a 400 rather than a stored empty row.
func TestAppURLRejectsMissingURL(t *testing.T) {
	_, base := appURLServer(t)

	res := postJSON(t, base, AppURLRequest{Label: "no target"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("blank url status = %d, want 400", res.StatusCode)
	}
	if len(listAppURLs(t, base)) != 0 {
		t.Error("rejected entry was stored anyway")
	}
}

// TestAppURLUniqueness covers both 4xx the store's (repo, workspace) constraint
// stands behind: a workspace can hold one entry, and a repo one default.
func TestAppURLUniqueness(t *testing.T) {
	_, base := appURLServer(t)
	createAppURL(t, base, AppURLRequest{URL: "http://localhost:3000"})
	createAppURL(t, base, AppURLRequest{URL: "http://localhost:3001", Workspace: "api"})

	cases := []struct {
		name string
		body AppURLRequest
	}{
		{"second default", AppURLRequest{URL: "http://localhost:4000"}},
		{"duplicate workspace", AppURLRequest{URL: "http://localhost:4001", Workspace: "api"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := postJSON(t, base, tc.body)
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want 409", res.StatusCode)
			}
		})
	}
	if len(listAppURLs(t, base)) != 2 {
		t.Error("a rejected create still landed in the store")
	}
}

// TestAppURLConcurrentCreateConflicts pins the backstop behind the ByWorkspace
// pre-check: that check and the insert are not atomic, so a writer can clear the
// check and still lose at the constraint. However the writes interleave, the
// answer must be one 201 and 409s for the rest — never a 500 carrying the
// driver's constraint text. Writers warm their own connection before the barrier
// so the posts land together rather than staggering on the dial, which is what
// opens the window at all.
func TestAppURLConcurrentCreateConflicts(t *testing.T) {
	_, base := appURLServer(t)
	body, err := json.Marshal(AppURLRequest{URL: "http://localhost:3000", Workspace: "burst"})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	const writers = 32
	codes := make([]int, writers)
	start := make(chan struct{})
	var ready, done sync.WaitGroup
	ready.Add(writers)
	done.Add(writers)
	for i := range writers {
		go func() {
			defer done.Done()
			client := &http.Client{Transport: &http.Transport{}}
			defer client.CloseIdleConnections()
			if warm, err := client.Get(base); err == nil {
				_ = warm.Body.Close()
			}
			ready.Done()

			<-start
			res, err := client.Post(base, "application/json", bytes.NewReader(body))
			if err != nil {
				t.Errorf("post: %v", err)
				return
			}
			defer func() { _ = res.Body.Close() }()
			codes[i] = res.StatusCode
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()

	created := 0
	for _, code := range codes {
		switch code {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
		default:
			t.Errorf("concurrent create status = %d, want 201 or 409", code)
		}
	}
	if created != 1 {
		t.Errorf("%d concurrent creates succeeded, want exactly 1", created)
	}
}

func TestAppURLUpdateAndDelete(t *testing.T) {
	_, base := appURLServer(t)
	created := createAppURL(t, base, AppURLRequest{URL: "http://localhost:3000"})
	other := createAppURL(t, base, AppURLRequest{URL: "http://localhost:3001", Workspace: "api"})
	item := base + "/" + strconv.FormatInt(created.ID, 10)

	res := putJSON(t, item, AppURLRequest{Label: "web", URL: "http://localhost:4000", Workspace: "web"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d, want 200", res.StatusCode)
	}
	var updated AppURLView
	if err := json.NewDecoder(res.Body).Decode(&updated); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	_ = res.Body.Close()
	if updated.URL != "http://localhost:4000" || updated.Workspace != "web" {
		t.Errorf("update returned %+v", updated)
	}

	clash := putJSON(t, item, AppURLRequest{URL: "http://localhost:4000", Workspace: "api"})
	_ = clash.Body.Close()
	if clash.StatusCode != http.StatusConflict {
		t.Errorf("re-scope onto a taken workspace status = %d, want 409", clash.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodDelete, item, nil)
	del, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	_ = del.Body.Close()
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", del.StatusCode)
	}

	getRes, _ := http.Get(item)
	_ = getRes.Body.Close()
	if getRes.StatusCode != http.StatusNotFound {
		t.Errorf("get after delete status = %d, want 404", getRes.StatusCode)
	}
	if list := listAppURLs(t, base); len(list) != 1 || list[0].ID != other.ID {
		t.Errorf("list after delete = %+v, want only the api workspace", list)
	}
}

func TestAppURLUnknownRepo(t *testing.T) {
	ts, _ := appURLServer(t)
	res, err := http.Get(ts.URL + APIPrefix + "/repos/ghost/app-urls")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown repo status = %d, want 404", res.StatusCode)
	}
}
