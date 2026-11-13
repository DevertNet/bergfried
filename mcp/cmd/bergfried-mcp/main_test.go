package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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

func TestInstructionsDescribeTaskRelease(t *testing.T) {
	for _, want := range []string{"release_ssh", "task_id", "once per task"} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("instructions do not contain %q", want)
		}
	}
}

func TestInstructionsDescribeSharedSkills(t *testing.T) {
	for _, want := range []string{"propose_skill_change", "~/.claude/skills"} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("instructions do not contain %q", want)
		}
	}
}

func TestProposeSkillChangeToolRegistered(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: "bergfried", Version: "test"}, nil)
	registerTools(s, &app{})
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	cs, err := c.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if tool.Name != "propose_skill_change" {
			continue
		}
		if !strings.Contains(tool.Description, "shared") || !strings.Contains(tool.Description, "read-only") {
			t.Fatalf("description = %q", tool.Description)
		}
		return
	}
	t.Fatal("propose_skill_change is not registered")
}

func TestSkillProposalRequestConvertsChanges(t *testing.T) {
	in := skillProposalIn{
		Reason: "improve the demo skill",
		Changes: []skillChangeIn{
			{Path: "demo/SKILL.md", Content: "new content\n"},
			{Path: "demo/old.txt", Delete: true},
		},
	}
	got := skillProposalRequest(in)
	if got.Reason != in.Reason || len(got.Changes) != 2 {
		t.Fatalf("request = %+v", got)
	}
	if c := got.Changes[0]; c.Path != "demo/SKILL.md" || c.Content != "new content\n" || c.Delete {
		t.Fatalf("change 0 = %+v", c)
	}
	if c := got.Changes[1]; c.Path != "demo/old.txt" || !c.Delete || c.Content != "" {
		t.Fatalf("change 1 = %+v", c)
	}
}
