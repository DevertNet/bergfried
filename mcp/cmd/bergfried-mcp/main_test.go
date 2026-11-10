package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorktreeIDFromWorkDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "work", "api", "sub")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	// OpenCode's shared server starts the MCP process with the project directory; the
	// environment may belong to a different client, so the working directory wins.
	t.Setenv("BERGFRIED_WORKTREE", "shop/main")
	if got := worktreeID(); got != "api" {
		t.Fatalf("worktreeID() = %q, want api", got)
	}
}

func TestWorktreeIDFallsBackToEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	t.Setenv("BERGFRIED_WORKTREE", "shop/main")
	if got := worktreeID(); got != "shop/main" {
		t.Fatalf("worktreeID() = %q", got)
	}
}

func TestWorktreeIDRejectsInvalidPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "work", "UNSICHER")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Setenv("BERGFRIED_WORKTREE", "")
	if got := worktreeID(); got != "" {
		t.Fatalf("invalid directory must not become a worktree name: %q", got)
	}
}
