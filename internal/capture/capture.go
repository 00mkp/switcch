//go:build darwin

// Package capture registers the currently logged-in Claude account: it copies
// the live default credential into switcch's own keychain store and records the
// account's identity in the registry.
package capture

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/00mkp/switcch/internal/account"
	"github.com/00mkp/switcch/internal/keychain"
)

// ErrNotLoggedIn means there is no live Claude Code credential to capture.
var ErrNotLoggedIn = errors.New("capture: no logged-in Claude account found (run `claude` and /login first)")

// Indirected for tests.
var (
	readLiveCred     = func() (string, error) { return keychain.Read(keychain.ServiceName("")) }
	readLiveIdentity = defaultReadLiveIdentity
	writeBackupCred  = func(slot int, cred string) error { return keychain.Write(account.CredService(slot), cred) }
)

// Capture reads the live credential, derives the account identity, stores the
// credential under the new slot's keychain service, and adds the identity to store.
func Capture(store *account.Store, alias string) (account.Account, error) {
	cred, err := readLiveCred()
	if err != nil {
		if errors.Is(err, keychain.ErrNotFound) {
			return account.Account{}, ErrNotLoggedIn
		}
		return account.Account{}, fmt.Errorf("capture: read live credential: %w", err)
	}
	ident, err := readLiveIdentity()
	if err != nil {
		return account.Account{}, fmt.Errorf("capture: read identity: %w", err)
	}
	ident.Alias = alias
	a, err := store.Add(ident)
	if err != nil {
		return account.Account{}, err
	}
	if err := writeBackupCred(a.Slot, cred); err != nil {
		return account.Account{}, fmt.Errorf("capture: store credential: %w", err)
	}
	return a, nil
}

// defaultReadLiveIdentity reads the oauthAccount block from ~/.claude.json (the
// default profile's identity). Best-effort: a missing file yields an empty identity.
func defaultReadLiveIdentity() (account.Account, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return account.Account{}, err
	}
	b, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		return account.Account{}, nil
	}
	var top struct {
		OAuthAccount struct {
			EmailAddress     string `json:"emailAddress"`
			AccountUUID      string `json:"accountUuid"`
			OrganizationUUID string `json:"organizationUuid"`
			DisplayName      string `json:"displayName"`
			OrganizationName string `json:"organizationName"`
		} `json:"oauthAccount"`
	}
	if err := json.Unmarshal(b, &top); err != nil {
		return account.Account{}, nil
	}
	oa := top.OAuthAccount
	return account.Account{
		Email:       oa.EmailAddress,
		AccountUUID: oa.AccountUUID,
		OrgUUID:     oa.OrganizationUUID,
		DisplayName: oa.DisplayName,
		OrgName:     oa.OrganizationName,
	}, nil
}
