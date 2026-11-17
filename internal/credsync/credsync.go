//go:build darwin

// Package credsync keeps switcch's stored credential as fresh as Claude Code's.
//
// Claude rotates the OAuth token into the profile's hashed keychain entry during
// a session. Phase 1/2 only seeded from a static file backup and never read that
// rotation back, so the backup drifted stale. Current reads the profile keychain
// first (Claude's freshest copy), falls back to the encrypted file backup, and
// syncs the backup when the keychain is newer — "read where Claude writes".
package credsync

import (
	"encoding/json"

	"github.com/00mkp/switcch/internal/account"
	"github.com/00mkp/switcch/internal/credblob"
	"github.com/00mkp/switcch/internal/credfile"
	"github.com/00mkp/switcch/internal/keychain"
	"github.com/00mkp/switcch/internal/profile"
)

// readProfileKeychain reads the account's current credential from its profile's
// hashed keychain entry (where Claude keeps it fresh). Test seam.
var readProfileKeychain = func(a account.Account) (string, error) {
	return keychain.Read(keychain.ServiceName(profile.Dir(a)))
}

// Current returns the freshest credential for the account: the profile keychain
// entry when it exists and is newer than the file backup (syncing the backup),
// else the file backup. Errors only when neither source is available.
func Current(a account.Account) (string, error) {
	kc, kerr := readProfileKeychain(a)
	file, ferr := credfile.Read(a.Slot)
	switch {
	case kerr == nil && ferr == nil:
		if expiresAtMs(kc) > expiresAtMs(file) {
			_ = SyncBackup(a, kc)
			return kc, nil
		}
		return file, nil
	case kerr == nil:
		_ = SyncBackup(a, kc)
		return kc, nil
	case ferr == nil:
		return file, nil
	default:
		return "", ferr
	}
}

// SyncBackup writes cred to the file backup, but only when it parses and carries
// an access token — never overwrite a good backup with an empty/garbage read.
func SyncBackup(a account.Account, cred string) error {
	blob, err := credblob.Parse([]byte(cred))
	if err != nil || blob.AccessToken() == "" {
		return nil
	}
	return credfile.Write(a.Slot, cred)
}

func expiresAtMs(cred string) int64 {
	var top struct {
		OAuth struct {
			ExpiresAt int64 `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal([]byte(cred), &top) != nil {
		return 0
	}
	return top.OAuth.ExpiresAt
}
