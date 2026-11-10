package lima

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanLine(t *testing.T) {
	cases := map[string]string{
		`time="2026-11-09T19:32:52+01:00" level=info msg="( 1/6) ✅ SSH connection"`:     "( 1/6) ✅ SSH connection",
		`time="x" level=warning msg="[hostagent] failed to listen tcp: 127.0.0.1:5355"`: "warning: [hostagent] failed to listen tcp: 127.0.0.1:5355",
		`time="x" level=info msg="Forwarding \"a\" to b"`:                               `Forwarding "a" to b`,
		`plain text line`: "plain text line",
	}
	for in, want := range cases {
		if got := CleanLine(in); got != want {
			t.Errorf("CleanLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func fakeLimactl(t *testing.T, script string) ExecRunner {
	p := filepath.Join(t.TempDir(), "limactl")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	return ExecRunner{Bin: p}
}

func TestRunLinesStreamsStderrLines(t *testing.T) {
	r := fakeLimactl(t, `echo 'time="x" level=info msg="step one"' >&2
echo 'time="x" level=info msg="step two"' >&2
echo stdout-data
`)
	var got []string
	ctx := WithOutput(context.Background(), func(l string) { got = append(got, l) })
	out, err := Client{R: r}.RunLines(ctx, "start", "x")
	if err != nil || strings.TrimSpace(string(out)) != "stdout-data" {
		t.Fatalf("%q %v", out, err)
	}
	if len(got) != 2 || got[0] != "step one" || got[1] != "step two" {
		t.Fatalf("lines: %v", got)
	}
}

func TestRunLinesErrorContainsOutput(t *testing.T) {
	r := fakeLimactl(t, `echo 'time="x" level=fatal msg="boom"' >&2
exit 1
`)
	var got []string
	ctx := WithOutput(context.Background(), func(l string) { got = append(got, l) })
	_, err := Client{R: r}.RunLines(ctx, "start", "x")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error must contain the output: %v", err)
	}
	if len(got) != 1 || got[0] != "fatal: boom" {
		t.Fatalf("lines: %v", got)
	}
}
