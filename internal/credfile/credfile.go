// Package credfile stores each account's backup credential as a 0600 file.
//
// Real credentials include every MCP/connector OAuth token (mcpOAuth) and can be
// ~20KB+, far past the macOS `security -i` ~4096-byte line limit, so the Keychain
// CLI cannot write them without exposing the secret on argv. A 0600 file matches
// the posture of the profile's seeded .credentials.json we already write; a cgo
// Security-framework (SecItemAdd) store would be the encrypted-at-rest upgrade.
package credfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrNotFound is returned by Read when no credential is stored for the slot.
var ErrNotFound = errors.New("credfile: no stored credential")

func dir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "switcch", "credentials")
}

// Path is the backup-credential file path for a slot.
func Path(slot int) string {
	return filepath.Join(dir(), fmt.Sprintf("%d.json", slot))
}

// Read returns the stored credential for the slot, or ErrNotFound.
func Read(slot int) (string, error) {
	b, err := os.ReadFile(Path(slot))
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Write stores cred for the slot atomically at mode 0600.
func Write(slot int, cred string) error {
	if err := os.MkdirAll(dir(), 0o700); err != nil {
		return err
	}
	tmp := Path(slot) + ".tmp"
	if err := os.WriteFile(tmp, []byte(cred), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, Path(slot))
}

// Delete removes the slot's credential file; a missing file is not an error.
func Delete(slot int) error {
	err := os.Remove(Path(slot))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
