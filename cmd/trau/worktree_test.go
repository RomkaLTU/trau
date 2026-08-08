package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/pipeline"
)

// TestBuildPipelineTargetsTheWorktreeAndKeepsRepoIdentity pins the wiring: git and delivery act on the given tree while RepoRoot — the key every
// hub record is written under — stays the registered root.
func TestBuildPipelineTargetsTheWorktreeAndKeepsRepoIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "shipflock")
	work := filepath.Join(t.TempDir(), "wt-COD-1580")
	for _, dir := range []string{root, work} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name     string
		worktree string
		wantGit  string
	}{
		{name: "no worktree", worktree: "", wantGit: root},
		{name: "worktree", worktree: work, wantGit: work},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{RepoRoot: root, WorkTree: tt.worktree, RunsDir: ".trau/runs"}
			p, err := buildPipeline(cfg, nil, root, nil, nil, nil, nil, nil, &sessionRecorder{}, nil)
			if err != nil {
				t.Fatalf("buildPipeline: %v", err)
			}
			git, ok := p.Git.(pipeline.ExecGit)
			if !ok {
				t.Fatalf("Git is %T, want pipeline.ExecGit", p.Git)
			}
			if git.Repo != tt.wantGit {
				t.Errorf("Git.Repo = %q, want %q", git.Repo, tt.wantGit)
			}
			gh, ok := p.Delivery.(pipeline.ExecGitHub)
			if !ok {
				t.Fatalf("Delivery is %T, want pipeline.ExecGitHub", p.Delivery)
			}
			if gh.Repo != tt.wantGit {
				t.Errorf("Delivery.Repo = %q, want %q", gh.Repo, tt.wantGit)
			}
			if p.RepoRoot != root {
				t.Errorf("RepoRoot = %q, want the registered root %q", p.RepoRoot, root)
			}
			if p.WorkTree != tt.worktree {
				t.Errorf("WorkTree = %q, want %q", p.WorkTree, tt.worktree)
			}
			if p.InternalPrefix == "" {
				t.Error("InternalPrefix is empty — the internal id prefix is derived from the registered repo name")
			}
		})
	}
}

// TestBuildBackendRunsTheAgentInTheWorktree covers the agent-phase cwd and the
// result-file channel: both follow the worktree, so a phase edits the given tree
// and hands its result back beside that tree's own run files.
func TestBuildBackendRunsTheAgentInTheWorktree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell stub is not an executable provider binary on Windows")
	}
	root := t.TempDir()
	work := t.TempDir()
	bin := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name          string
		worktree      string
		wantDir       string
		wantResultDir string
	}{
		{name: "no worktree", worktree: "", wantDir: root, wantResultDir: ".trau/runs"},
		{name: "worktree", worktree: work, wantDir: work, wantResultDir: filepath.Join(work, ".trau/runs")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{RepoRoot: root, WorkTree: tt.worktree, RunsDir: ".trau/runs", ClaudeBin: bin}
			r, err := buildBackend(agent.DefaultRegistry(), cfg, "claude", "", "", "build", &event.Log{}, nil, nil, nil, nil)
			if err != nil {
				t.Fatalf("buildBackend: %v", err)
			}
			ci, ok := r.(*agent.ClaudeInteractive)
			if !ok {
				t.Fatalf("backend is %T, want *agent.ClaudeInteractive", r)
			}
			if ci.Dir != tt.wantDir {
				t.Errorf("agent cwd = %q, want %q", ci.Dir, tt.wantDir)
			}
			if ci.ResultDir != tt.wantResultDir {
				t.Errorf("agent result dir = %q, want %q", ci.ResultDir, tt.wantResultDir)
			}
		})
	}
}

// TestDeclaredForgeAnswersForTheWorktree keeps the run's own layered FORGE in
// force for the tree it works in: only a Folder repo's child re-reads a forge
// from a .trau.ini on disk.
func TestDeclaredForgeAnswersForTheWorktree(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, config.ProjectConfigName), []byte("FORGE=bitbucket\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{RepoRoot: root, WorkTree: work, Forge: "github"}
	if got := declaredForge(cfg, root, work); got != "github" {
		t.Fatalf("declaredForge(worktree) = %q, want github — the worktree must not re-derive a config layer", got)
	}
}
