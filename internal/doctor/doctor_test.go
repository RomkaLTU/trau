package doctor

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/tracker/jiraapi"
)

func newTestRunner() *runner {
	return &runner{w: &writer{out: &bytes.Buffer{}}, r: &Report{}}
}

func lastCheck(t *testing.T, rr *runner) Check {
	t.Helper()
	if len(rr.r.Checks) == 0 {
		t.Fatal("no checks recorded")
	}
	return rr.r.Checks[len(rr.r.Checks)-1]
}

func TestCheckJiraSkippedForNonJira(t *testing.T) {
	rr := newTestRunner()
	checkJira(context.Background(), config.Config{TrackerProvider: "linear"}, rr)
	if len(rr.r.Checks) != 0 {
		t.Errorf("expected no jira check for linear provider, got %+v", rr.r.Checks)
	}
}

func TestCheckJiraWarnsOnMissingKeys(t *testing.T) {
	rr := newTestRunner()
	checkJira(context.Background(), config.Config{TrackerProvider: "jira", JiraBaseURL: "https://acme.atlassian.net"}, rr)
	c := lastCheck(t, rr)
	if c.Status != warn {
		t.Errorf("status = %q, want warn", c.Status)
	}
	if rr.r.Warnings != 1 || rr.r.Errors != 0 {
		t.Errorf("warnings=%d errors=%d, want 1/0", rr.r.Warnings, rr.r.Errors)
	}
	if !strings.Contains(c.Message, "JIRA_EMAIL") || !strings.Contains(c.Message, "JIRA_API_TOKEN") {
		t.Errorf("message %q should name the missing keys", c.Message)
	}
}

func TestCheckJiraLiveAuthPass(t *testing.T) {
	const token = "s3cr3t-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/myself" {
			t.Errorf("ping path = %q, want /rest/api/3/myself", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"accountId":"x"}`))
	}))
	defer srv.Close()

	rr := newTestRunner()
	checkJira(context.Background(), config.Config{
		TrackerProvider: "jira", JiraBaseURL: srv.URL, JiraEmail: "me@acme.com", JiraAPIToken: token,
	}, rr)
	c := lastCheck(t, rr)
	if c.Status != pass {
		t.Errorf("status = %q msg=%q, want pass", c.Status, c.Message)
	}
	if strings.Contains(c.Message, token) {
		t.Errorf("check message leaked the token: %q", c.Message)
	}
}

func TestCheckJiraLiveAuth401(t *testing.T) {
	const token = "s3cr3t-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	rr := newTestRunner()
	checkJira(context.Background(), config.Config{
		TrackerProvider: "jira", JiraBaseURL: srv.URL, JiraEmail: "me@acme.com", JiraAPIToken: token,
	}, rr)
	c := lastCheck(t, rr)
	if c.Status != fail {
		t.Errorf("status = %q, want fail", c.Status)
	}
	if rr.r.Errors != 1 {
		t.Errorf("errors = %d, want 1", rr.r.Errors)
	}
	if !strings.Contains(c.Message, jiraapi.TokenHelpURL) {
		t.Errorf("401 message %q should carry the regenerate URL", c.Message)
	}
	if strings.Contains(c.Message, token) {
		t.Errorf("check message leaked the token: %q", c.Message)
	}
}
