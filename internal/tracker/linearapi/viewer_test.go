package linearapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestViewerReturnsIdentity(t *testing.T) {
	var req graphReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"viewer":{"id":"u-1","name":"Ada Lovelace"}}}`)
	}))
	defer srv.Close()

	c := New("lin_key")
	c.Endpoint = srv.URL
	id, name, err := c.Viewer(context.Background())
	if err != nil {
		t.Fatalf("Viewer: %v", err)
	}
	if !strings.Contains(req.Query, "Viewer") {
		t.Fatalf("query = %q, want the viewer query", req.Query)
	}
	if id != "u-1" || name != "Ada Lovelace" {
		t.Fatalf("viewer = %q/%q, want u-1/Ada Lovelace", id, name)
	}
}

func TestViewerDisabledWithoutKey(t *testing.T) {
	if _, _, err := New("").Viewer(context.Background()); !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("Viewer disabled = %v, want ErrNotEnabled", err)
	}
}
