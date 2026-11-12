package credfile

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

// useTestKey pins a fixed 32-byte key so encryption is deterministic in tests.
func useTestKey(t *testing.T) {
	t.Helper()
	orig := loadKey
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	loadKey = func() ([]byte, error) { return key, nil }
	t.Cleanup(func() { loadKey = orig })
}

func TestRoundTripLargePayload(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	useTestKey(t)

	big := strings.Repeat("x", 25000) // past the keychain `security -i` line limit
	if err := Write(1, big); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(Path(1))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, want 0600", fi.Mode().Perm())
	}
	got, err := Read(1)
	if err != nil {
		t.Fatal(err)
	}
	if got != big {
		t.Fatalf("round-trip mismatch: len got %d, want %d", len(got), len(big))
	}
	// On disk it must be ciphertext, not the plaintext.
	raw, _ := os.ReadFile(Path(1))
	if bytes.Contains(raw, []byte(strings.Repeat("x", 100))) {
		t.Fatal("file contains plaintext; not encrypted at rest")
	}
}

func TestFileHidesSecret(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	useTestKey(t)
	if err := Write(3, `{"token":"topsecret-value"}`); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(Path(3))
	if bytes.Contains(raw, []byte("topsecret-value")) {
		t.Fatal("secret appears in plaintext in the file")
	}
}

func TestTamperedCiphertextFails(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	useTestKey(t)
	if err := Write(4, "hello"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(Path(4))
	raw[len(raw)-1] ^= 0xff // flip last byte
	if err := os.WriteFile(Path(4), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(4); err == nil {
		t.Fatal("tampered ciphertext should fail authentication, got nil error")
	}
}

func TestLegacyPlaintextMigrates(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	useTestKey(t)
	// Simulate a pre-encryption plaintext JSON file written directly.
	if err := os.MkdirAll(dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"claudeAiOauth":{"accessToken":"AT"}}`
	if err := os.WriteFile(Path(5), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Read(5)
	if err != nil {
		t.Fatal(err)
	}
	if got != legacy {
		t.Fatalf("legacy read = %q, want %q", got, legacy)
	}
	// The file must now be re-encrypted (no longer the plaintext).
	raw, _ := os.ReadFile(Path(5))
	if bytes.Equal(raw, []byte(legacy)) {
		t.Fatal("legacy file was not migrated to ciphertext")
	}
	// And a subsequent read still works.
	if again, err := Read(5); err != nil || again != legacy {
		t.Fatalf("post-migration read = %q err %v", again, err)
	}
}

func TestReadMissingIsNotFound(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if _, err := Read(7); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Read of missing = %v, want ErrNotFound", err)
	}
}

func TestDeleteToleratesMissing(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	useTestKey(t)
	if err := Write(2, "x"); err != nil {
		t.Fatal(err)
	}
	if err := Delete(2); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after Delete: %v, want ErrNotFound", err)
	}
	if err := Delete(2); err != nil {
		t.Fatalf("Delete of absent should be nil, got %v", err)
	}
}
