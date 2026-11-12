package credfile

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestRoundTripLargePayload(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	// Larger than the ~4096-byte keychain `security -i` line limit that forced
	// file-based storage in the first place.
	big := strings.Repeat("x", 25000)
	if err := Write(1, big); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(Path(1))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("credential file mode = %v, want 0600", fi.Mode().Perm())
	}
	got, err := Read(1)
	if err != nil {
		t.Fatal(err)
	}
	if got != big {
		t.Fatalf("round-trip mismatch: len got %d, want %d", len(got), len(big))
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
