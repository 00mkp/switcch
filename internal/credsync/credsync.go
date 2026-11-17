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
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/00mkp/switcch/internal/account"
	"github.com/00mkp/switcch/internal/credblob"
	"github.com/00mkp/switcch/internal/credfile"
	"github.com/00mkp/switcch/internal/keychain"
	"github.com/00mkp/switcch/internal/profile"
)

// ErrCannotRecover means the account is not the current live login, so switcch
// cannot safely re-capture its credential.
var ErrCannotRecover = errors.New("credsync: cannot recover (account is not the live login)")

// Test seams for self-heal.
var (
	readLiveIdentityEmail = defaultLiveEmail
	readLiveDefaultCred   = func() (string, error) { return keychain.Read(keychain.ServiceName("")) }
)

// Recover re-captures a dead backup from the live default login, but only when
// that login's email matches the account — never capture one account's token
// under another's slot. Returns ErrCannotRecover otherwise.
func Recover(a account.Account) (string, error) {
	email, err := readLiveIdentityEmail()
	if err != nil {
		return "", err
	}
	if a.Email == "" || !strings.EqualFold(email, a.Email) {
		return "", ErrCannotRecover
	}
	cred, err := readLiveDefaultCred()
	if err != nil {
		return "", err
	}
	if err := SyncBackup(a, cred); err != nil {
		return "", err
	}
	return cred, nil
}

// defaultLiveEmail reads the live default login's email from ~/.claude.json.
func defaultLiveEmail() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		return "", nil
	}
	var top struct {
		OAuth struct {
			Email string `json:"emailAddress"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal(b, &top) != nil {
		return "", nil
	}
	return top.OAuth.Email, nil
}

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
	// Write-if-newer: a late capture-back (outside the refresh lock) must never
	// move the backup backward to an older token whose refresh token may already
	// be spent by a concurrent refresh.
	if onDisk, rerr := credfile.Read(a.Slot); rerr == nil && expiresAtMs(onDisk) >= expiresAtMs(cred) {
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
