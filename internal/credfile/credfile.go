// Package credfile stores each account's backup credential as an encrypted file.
//
// Real credentials include every MCP/connector OAuth token (mcpOAuth) and can be
// ~10-20KB, far past the macOS `security -i` ~4096-byte line limit, so the
// Keychain CLI cannot write them without exposing the secret on argv. Instead the
// credential is AES-256-GCM-encrypted into a 0600 file under a key held in the
// Keychain (see key_darwin.go) — encrypted at rest, any size, no cgo. A legacy
// plaintext file (pre-encryption) is migrated to ciphertext on first read.
package credfile

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrNotFound is returned by Read when no credential is stored for the slot.
var ErrNotFound = errors.New("credfile: no stored credential")

// loadKey returns the 32-byte AES key. The default has no platform key store;
// a platform file (key_darwin.go) overrides it.
var loadKey = func() ([]byte, error) {
	return nil, errors.New("credfile: no key provider for this platform")
}

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

func seal(key, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func open(key, data []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(data) < ns {
		return nil, errors.New("credfile: ciphertext too short")
	}
	return gcm.Open(nil, data[:ns], data[ns:], nil)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Read returns the decrypted credential for the slot, or ErrNotFound. A legacy
// plaintext-JSON file is transparently re-encrypted in place and returned.
func Read(slot int) (string, error) {
	raw, err := os.ReadFile(Path(slot))
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	key, err := loadKey()
	if err != nil {
		return "", err
	}
	pt, err := open(key, raw)
	if err != nil {
		// Migration: a pre-encryption file is plaintext JSON. Require VALID JSON,
		// not just a leading '{': a random nonce starts with '{' 1 time in 256,
		// and a corrupt ciphertext must fail rather than become a "credential".
		if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) && json.Valid(raw) {
			if werr := Write(slot, string(raw)); werr != nil {
				return "", fmt.Errorf("credfile: migrate slot %d: %w", slot, werr)
			}
			return string(raw), nil
		}
		return "", fmt.Errorf("credfile: decrypt slot %d: %w", slot, err)
	}
	return string(pt), nil
}

// Write encrypts and stores cred for the slot atomically at mode 0600.
func Write(slot int, cred string) error {
	key, err := loadKey()
	if err != nil {
		return err
	}
	ct, err := seal(key, []byte(cred))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir(), 0o700); err != nil {
		return err
	}
	tmp := Path(slot) + ".tmp"
	if err := os.WriteFile(tmp, ct, 0o600); err != nil {
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
