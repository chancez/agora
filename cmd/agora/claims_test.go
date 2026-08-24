package main

import (
	"strings"
	"testing"

	"github.com/chancez/agora/internal/store"
)

func TestClaims(t *testing.T) {
	alice := newCLI(t)
	bob := alice.as("bob")

	if got := alice.mustRun("claims"); !strings.Contains(got.stdout, "[]") {
		t.Errorf("agora claims on an unused channel printed %q, want []", got.stdout)
	}
	alice.mustRun("claim", "parser-panic", "--note", "on it", "--paths", "parser.go,internal/lex/**")
	bob.mustRun("claim", "lexer-bounds")

	got := decode[[]store.Claim](t, alice.mustRun("claims"))
	if len(got) != 2 {
		t.Fatalf("agora claims returned %d claims, want 2: %+v", len(got), got)
	}
	text := alice.mustRun("claims", "--text").stdout
	for _, want := range []string{"parser-panic", "alice", "parser.go, internal/lex/**", "on it", "lexer-bounds", "bob"} {
		if !strings.Contains(text, want) {
			t.Errorf("agora claims --text is missing %q:\n%s", want, text)
		}
	}
}
