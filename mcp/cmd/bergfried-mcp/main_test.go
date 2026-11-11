package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFolderIDFromWorkDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "work", "api", "sub")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	// OpenCode's shared server starts the MCP process with the project directory; the
	// environment may belong to a different client, so the working directory wins.
	t.Setenv("BERGFRIED_FOLDER", "shop/main")
	if got := folderID(); got != "work/api/sub" {
		t.Fatalf("folderID() = %q, want work/api/sub", got)
	}
}

func TestFolderIDFallsBackToEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	t.Setenv("BERGFRIED_FOLDER", "shop/main")
	if got := folderID(); got != "shop/main" {
		t.Fatalf("folderID() = %q", got)
	}
}

func TestFolderIDRejectsInvalidPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "work", "UNSICHER")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Setenv("BERGFRIED_FOLDER", "")
	if got := folderID(); got != "" {
		t.Fatalf("invalid directory must not become a folder name: %q", got)
	}
}
