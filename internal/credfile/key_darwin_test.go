//go:build darwin

package credfile

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/00mkp/switcch/internal/keychain"
)

func TestKeychainLoadKeyGetOrCreate(t *testing.T) {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	orig := masterKeyService
	masterKeyService = "switcch-test-mk-" + hex.EncodeToString(b) // throwaway, never the real one
	t.Cleanup(func() {
		_ = keychain.Delete(masterKeyService)
		masterKeyService = orig
	})

	k1, err := keychainLoadKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(k1) != 32 {
		t.Fatalf("key length = %d, want 32", len(k1))
	}
	k2, err := keychainLoadKey()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(k1, k2) {
		t.Fatal("key not stable: second get-or-create returned a different key")
	}
}
