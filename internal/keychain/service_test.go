package keychain

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"golang.org/x/text/unicode/norm"
)

// first8SHA256 computes the recipe independently so the test does not just
// restate the implementation.
func first8SHA256(s string) string {
	sum := sha256.Sum256([]byte(norm.NFC.String(s)))
	return hex.EncodeToString(sum[:])[:8]
}

func TestServiceName(t *testing.T) {
	if got, want := ServiceName("/tmp/x"), "Claude Code-credentials-"+first8SHA256("/tmp/x"); got != want {
		t.Fatalf("ServiceName(%q) = %q, want %q", "/tmp/x", got, want)
	}
	if got := ServiceName(""); got != "Claude Code-credentials" {
		t.Fatalf("ServiceName(\"\") = %q, want unsuffixed %q", got, "Claude Code-credentials")
	}
	// NFC: composed and decomposed forms must hash identically.
	if ServiceName("café") != ServiceName("café") {
		t.Fatal("NFC not applied: composed and decomposed forms produced different services")
	}
}

func TestAccountName(t *testing.T) {
	t.Setenv("USER", "alice")
	if got := AccountName(); got != "alice" {
		t.Fatalf("AccountName() with USER=alice = %q, want alice", got)
	}
	// A USER value with characters outside [A-Za-z0-9._-] falls back to the constant.
	t.Setenv("USER", "bad user!")
	if got := AccountName(); got != "claude-code-user" {
		t.Fatalf("AccountName() with junk USER = %q, want claude-code-user", got)
	}
}
