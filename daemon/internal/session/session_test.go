package session

import (
	"context"
	"strings"
	"testing"
)

func TestValidName(t *testing.T) {
	for _, ok := range []string{"shop", "a", "my-app-2"} {
		if ValidName(ok) != nil {
			t.Fatal(ok)
		}
	}
	for _, bad := range []string{"", "Shop", "1a", "a b", "a;rm -rf", "a'b", "../x", strings.Repeat("a", 40)} {
		if ValidName(bad) == nil {
			t.Fatalf("%q muss abgelehnt werden", bad)
		}
	}
}

func TestAgentCmdIsolatesComposeProject(t *testing.T) {
	c := AgentCmd("shop", "api")
	if !strings.Contains(c, "COMPOSE_PROJECT_NAME=shop-api") || !strings.Contains(c, "--remote-control 'shop/api'") {
		t.Fatal(c)
	}
}

func TestErrUnpushed(t *testing.T) {
	var err error = ErrUnpushed{Report: "x"}
	if !strings.Contains(err.Error(), "--force") {
		t.Fatal("Hinweis auf --force fehlt")
	}
	_ = context.Background()
}
