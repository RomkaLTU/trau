package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetryableGH(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"opaque exit status", errors.New("exit status 1"), true},
		{"network timeout", errors.New("dial tcp: i/o timeout"), true},
		{"secondary rate limit", errors.New("exit status 1: You have exceeded a secondary rate limit"), true},
		{"no commits between", errors.New("gh pr create: exit status 1: No commits between main and feature/x"), false},
		{"already exists", errors.New("a pull request for branch already exists"), false},
		{"unauthorized", errors.New("HTTP 401: Unauthorized"), false},
		{"not mergeable", errors.New("Pull request is not mergeable"), false},
	}
	for _, c := range cases {
		if got := retryableGH(c.err); got != c.want {
			t.Errorf("%s: retryableGH=%v want %v", c.name, got, c.want)
		}
	}
}

func TestRetryGHRetriesTransientThenSucceeds(t *testing.T) {
	p := &Pipeline{Sleep: func(time.Duration) {}}
	calls := 0
	err := p.retryGH(context.Background(), "op", func() error {
		calls++
		if calls < 3 {
			return errors.New("exit status 1")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("want nil after retry, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("want 3 attempts, got %d", calls)
	}
}

func TestRetryGHDeterministicFailsFast(t *testing.T) {
	p := &Pipeline{Sleep: func(time.Duration) {}}
	calls := 0
	err := p.retryGH(context.Background(), "op", func() error {
		calls++
		return errors.New("gh pr create: exit status 1: No commits between main and feature/x")
	})
	if err == nil {
		t.Fatal("want error")
	}
	if calls != 1 {
		t.Fatalf("want 1 attempt (fail fast), got %d", calls)
	}
}

func TestRetryGHExhaustsRetries(t *testing.T) {
	p := &Pipeline{Sleep: func(time.Duration) {}}
	calls := 0
	err := p.retryGH(context.Background(), "op", func() error {
		calls++
		return errors.New("transient boom")
	})
	if err == nil {
		t.Fatal("want error after exhausting retries")
	}
	if calls != 3 {
		t.Fatalf("want 3 attempts, got %d", calls)
	}
}

func TestCreateOrAdoptPRAdoptsExistingOnError(t *testing.T) {
	fake := &fakeGitHub{createErr: []error{errors.New("exit status 1")}, prURL: "https://x/pr/99"}
	p := &Pipeline{GitHub: fake, Sleep: func(time.Duration) {}}
	url, err := p.createOrAdoptPR(context.Background(), "main", "feature/x", "t", "b")
	if err != nil {
		t.Fatalf("want nil, got %v", err)
	}
	if url != "https://x/pr/99" {
		t.Fatalf("want adopted URL, got %q", url)
	}
	if fake.createCalls != 1 {
		t.Fatalf("want 1 create call (no duplicate), got %d", fake.createCalls)
	}
}

func TestCreateOrAdoptPRRetriesTransientThenCreates(t *testing.T) {
	fake := &fakeGitHub{createErr: []error{errors.New("exit status 1")}, createURL: "https://x/pr/100"}
	p := &Pipeline{GitHub: fake, Sleep: func(time.Duration) {}}
	url, err := p.createOrAdoptPR(context.Background(), "main", "feature/x", "t", "b")
	if err != nil {
		t.Fatalf("want nil, got %v", err)
	}
	if url != "https://x/pr/100" {
		t.Fatalf("want created URL, got %q", url)
	}
	if fake.createCalls != 2 {
		t.Fatalf("want 2 create attempts, got %d", fake.createCalls)
	}
}

type fakeGitHub struct {
	createErr   []error
	createCalls int
	createURL   string
	prURL       string
	prURLErr    error
}

func (f *fakeGitHub) PRURL(context.Context, string) (string, error) { return f.prURL, f.prURLErr }

func (f *fakeGitHub) CreatePR(context.Context, string, string, string, string) (string, error) {
	i := f.createCalls
	f.createCalls++
	if i < len(f.createErr) && f.createErr[i] != nil {
		return "", f.createErr[i]
	}
	return f.createURL, nil
}

func (f *fakeGitHub) PRState(context.Context, string) (string, error)   { return "", nil }
func (f *fakeGitHub) Checks(context.Context, string) ([]Check, error)   { return nil, nil }
func (f *fakeGitHub) Merge(context.Context, string, string, bool) error { return nil }
