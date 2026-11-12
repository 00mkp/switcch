package profile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/00mkp/switcch/internal/account"
	"github.com/00mkp/switcch/internal/keychain"
)

// deleteKeychain is indirected so tests can observe/stub it.
var defaultDeleteKeychain = func(service string) error { return keychain.Delete(service) }
var deleteKeychain = defaultDeleteKeychain

// Seed prepares an account's profile directory: it clears any stale hashed
// Keychain entry (so Claude reads our seed, not a leftover), writes the
// credential to .credentials.json (0600), and merges identity + onboarding
// flags into .claude.json.
func Seed(a account.Account, credJSON string) error {
	dir := Dir(a)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := deleteKeychain(keychain.ServiceName(dir)); err != nil {
		return fmt.Errorf("profile: clear stale keychain entry: %w", err)
	}
	if err := atomicWrite(filepath.Join(dir, ".credentials.json"), []byte(credJSON), 0o600); err != nil {
		return err
	}
	return mergeClaudeJSON(dir, a)
}

func mergeClaudeJSON(dir string, a account.Account) error {
	p := filepath.Join(dir, ".claude.json")
	m := map[string]any{}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	m["oauthAccount"] = map[string]any{
		"emailAddress":     a.Email,
		"accountUuid":      a.AccountUUID,
		"organizationUuid": a.OrgUUID,
		"displayName":      a.DisplayName,
		"organizationName": a.OrgName,
	}
	m["hasCompletedOnboarding"] = true
	if _, ok := m["theme"]; !ok {
		m["theme"] = "dark"
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(p, b, 0o600)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
