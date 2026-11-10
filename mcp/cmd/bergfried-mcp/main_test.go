package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAgentIDFromWorkDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "work", "api", "sub")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	// OpenCode's shared server starts the MCP process with the project directory; the
	// environment may belong to a different client, so the working directory wins.
	t.Setenv("BERGFRIED_AGENT", "shop/main")
	if got := agentID(); got != "api" {
		t.Fatalf("agentID() = %q, want api", got)
	}
}

func TestAgentIDFallsBackToEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	t.Setenv("BERGFRIED_AGENT", "shop/main")
	if got := agentID(); got != "shop/main" {
		t.Fatalf("agentID() = %q", got)
	}
}

func TestAgentIDRejectsInvalidPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "work", "UNSICHER")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Setenv("BERGFRIED_AGENT", "")
	if got := agentID(); got != "" {
		t.Fatalf("invalid directory must not become an agent name: %q", got)
	}
}
