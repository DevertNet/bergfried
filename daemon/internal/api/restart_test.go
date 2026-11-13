package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/DevertNet/bergfried/daemon/internal/sandbox"
	"github.com/DevertNet/bergfried/daemon/internal/store"
)

func TestRestartReasonsFor(t *testing.T) {
	cur := store.HelperStamp{Guidance: "g", MCP: "m"}
	same := map[string]store.HelperStamp{"t": cur}
	diff := map[string]store.HelperStamp{"t": {Guidance: "x", MCP: "y"}}
	other := map[string]store.HelperStamp{"other": {Guidance: "x", MCP: "y"}}
	cases := []struct {
		name    string
		running bool
		stamps  map[string]store.HelperStamp
		want    []string
	}{
		{name: "not running without stamp", running: false, stamps: nil, want: []string{}},
		{name: "not running with stamp", running: false, stamps: diff, want: []string{}},
		{name: "running without stamp", running: true, stamps: nil, want: []string{"unknown"}},
		{name: "running with other target only", running: true, stamps: other, want: []string{"unknown"}},
		{name: "running with equal stamp", running: true, stamps: same, want: []string{}},
		{name: "running with different stamp", running: true, stamps: diff, want: []string{"guidance", "mcp"}},
	}
	for _, c := range cases {
		got := restartReasonsFor(c.running, c.stamps, "t", cur)
		if got == nil {
			t.Fatalf("%s: reasons must be non-nil", c.name)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestOpenCodeRestartReasons(t *testing.T) {
	cur := store.HelperStamp{Guidance: "g", MCP: "m"}
	same := map[string]store.HelperStamp{sandbox.OpenCodeTarget: cur}
	diff := map[string]store.HelperStamp{sandbox.OpenCodeTarget: {Guidance: "x", MCP: "y"}}
	cases := []struct {
		name    string
		wanted  bool
		running bool
		stamps  map[string]store.HelperStamp
		want    []string
	}{
		{name: "not wanted but running without stamp", wanted: false, running: true, stamps: nil, want: []string{}},
		{name: "not wanted but running with different stamp", wanted: false, running: true, stamps: diff, want: []string{}},
		{name: "wanted and running without stamp", wanted: true, running: true, stamps: nil, want: []string{"unknown"}},
		{name: "wanted but not running", wanted: true, running: false, stamps: nil, want: []string{}},
		{name: "wanted and running with equal stamp", wanted: true, running: true, stamps: same, want: []string{}},
		{name: "wanted and running with different stamp", wanted: true, running: true, stamps: diff, want: []string{"guidance", "mcp"}},
	}
	for _, c := range cases {
		got := opencodeRestartReasons(c.wanted, c.running, c.stamps, cur)
		if got == nil {
			t.Fatalf("%s: reasons must be non-nil", c.name)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestClaudeJRestartReasonsJSON(t *testing.T) {
	b, err := json.Marshal(claudeJ{RestartReasons: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"restart_reasons":[]`) {
		t.Fatalf("restart_reasons must be an empty array: %s", b)
	}
}

func TestSandboxJOpenCodeRestartReasonsJSON(t *testing.T) {
	j := sandboxJ{Name: "shop", ClaudeSessions: []claudeJ{}, OpenCodeRestartReasons: []string{}}
	b, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"opencode_restart_reasons":[]`) {
		t.Fatalf("opencode_restart_reasons must be an empty array: %s", b)
	}
	if !strings.Contains(string(b), `"claude_sessions":[]`) {
		t.Fatalf("claude_sessions must be an empty array: %s", b)
	}
}
