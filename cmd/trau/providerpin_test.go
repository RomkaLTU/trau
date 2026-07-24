package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubclient"
)

type namedRunner struct{ name string }

func (r namedRunner) Run(context.Context, string, string) (agent.Result, error) {
	return agent.Result{}, nil
}

// pinHub serves the hub's by-id issue read with a fixed pin, so the selector can
// be exercised without a live hub.
func pinHub(t *testing.T, pin string) *hubclient.Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"COD-1","provider_pin":"` + pin + `"}`))
	}))
	t.Cleanup(ts.Close)
	return hubclient.New(ts.URL, "")
}

func TestRunnerSelectorPrefersThePinOverTheConfigDefault(t *testing.T) {
	cfg := config.Config{Provider: "claude"}
	def := namedRunner{name: "claude"}
	built := []string{}
	build := func(provider string) (agent.Runner, error) {
		built = append(built, provider)
		return namedRunner{name: provider}, nil
	}

	sel := newRunnerSelector(cfg, "", "acme", pinHub(t, "codex"), def, build)
	runner, provider, err := sel(context.Background(), "COD-1")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if provider != "codex" || runner.(namedRunner).name != "codex" {
		t.Fatalf("selected %q/%v, want the pinned codex", provider, runner)
	}

	if _, _, err := sel(context.Background(), "COD-1"); err != nil {
		t.Fatalf("second select: %v", err)
	}
	if len(built) != 1 {
		t.Fatalf("builds = %v, want the backend built once and reused", built)
	}
}

func TestRunnerSelectorKeepsTheDefaultWhenUnpinned(t *testing.T) {
	cfg := config.Config{Provider: "claude"}
	def := namedRunner{name: "claude"}
	build := func(string) (agent.Runner, error) {
		t.Fatal("unpinned ticket must reuse the run's default backend")
		return nil, nil
	}

	sel := newRunnerSelector(cfg, "", "acme", pinHub(t, ""), def, build)
	runner, provider, err := sel(context.Background(), "COD-1")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if provider != "claude" || runner != agent.Runner(def) {
		t.Fatalf("selected %q/%v, want the configured default", provider, runner)
	}
}

func TestRunnerSelectorLetsAnExplicitOverrideWin(t *testing.T) {
	// --provider lands in cfg.Provider too, so the override the selector honors is
	// the same name the default backend was built for.
	cfg := config.Config{Provider: "claude"}
	def := namedRunner{name: "claude"}
	build := func(string) (agent.Runner, error) {
		t.Fatal("an explicit override must not consult the ticket's pin")
		return nil, nil
	}

	sel := newRunnerSelector(cfg, "claude", "acme", pinHub(t, "codex"), def, build)
	_, provider, err := sel(context.Background(), "COD-1")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if provider != "claude" {
		t.Fatalf("selected %q, want the one-shot override to beat the pin", provider)
	}
}

func TestRunnerSelectorReportsAnUnbuildablePin(t *testing.T) {
	cfg := config.Config{Provider: "claude"}
	build := func(provider string) (agent.Runner, error) {
		return nil, errors.New("provider " + provider + ": not found on PATH")
	}

	sel := newRunnerSelector(cfg, "", "acme", pinHub(t, "codex"), namedRunner{}, build)
	_, provider, err := sel(context.Background(), "COD-1")
	if provider != "codex" {
		t.Fatalf("provider = %q, want the pin named on the failure", provider)
	}
	if err == nil || !strings.Contains(err.Error(), "codex") {
		t.Fatalf("err = %v, want it to name the pinned provider", err)
	}
}

func TestRunnerSelectorFallsBackWhenTheHubCannotAnswer(t *testing.T) {
	cfg := config.Config{Provider: "claude"}
	def := namedRunner{name: "claude"}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)

	sel := newRunnerSelector(cfg, "", "acme", hubclient.New(ts.URL, ""), def, func(string) (agent.Runner, error) {
		t.Fatal("an unreadable pin must leave the run on its configured default")
		return nil, nil
	})
	_, provider, err := sel(context.Background(), "COD-1")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if provider != "claude" {
		t.Fatalf("provider = %q, want the configured default", provider)
	}
}
