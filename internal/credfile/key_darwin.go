//go:build darwin

package credfile

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/00mkp/switcch/internal/keychain"
)

// masterKeyService is the Keychain service holding switcch's AES master key.
// Overridable in tests. The key is small (64 hex chars) so it fits the
// `security -i` line limit, and it is read/written via the stable `security`
// binary, so accessing it never triggers a per-rebuild keychain prompt.
var masterKeyService = "switcch-master-key"

func init() { loadKey = keychainLoadKey }

// keychainLoadKey returns the 32-byte AES master key, creating and storing a new
// random one on first use.
func keychainLoadKey() ([]byte, error) {
	v, err := keychain.Read(masterKeyService)
	if err == nil {
		key, derr := hex.DecodeString(v)
		if derr != nil || len(key) != 32 {
			return nil, fmt.Errorf("credfile: stored master key is corrupt")
		}
		return key, nil
	}
	if !errors.Is(err, keychain.ErrNotFound) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := keychain.Write(masterKeyService, hex.EncodeToString(key)); err != nil {
		return nil, err
	}
	return key, nil
}
