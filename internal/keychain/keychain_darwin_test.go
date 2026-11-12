//go:build darwin

package keychain

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"
)

func randHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func TestKeychainRoundTrip(t *testing.T) {
	// Throwaway service so this never touches the real "Claude Code-credentials".
	svc := "switcch-test-" + randHex(t)
	t.Cleanup(func() { _ = Delete(svc) })

	if _, err := Read(svc); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Read of absent item: err = %v, want ErrNotFound", err)
	}
	if err := Write(svc, `{"a":1}`); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := Read(svc)
	if err != nil {
		t.Fatalf("Read after Write: %v", err)
	}
	if got != `{"a":1}` {
		t.Fatalf("Read = %q, want %q", got, `{"a":1}`)
	}
	if err := Delete(svc); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := Read(svc); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Read after Delete: err = %v, want ErrNotFound", err)
	}
}
