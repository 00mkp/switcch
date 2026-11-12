//go:build darwin

// Package launch prepares an account's profile and execs Claude Code into it.
package launch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/00mkp/switcch/internal/account"
	"github.com/00mkp/switcch/internal/credblob"
	"github.com/00mkp/switcch/internal/keychain"
	"github.com/00mkp/switcch/internal/oauth"
	"github.com/00mkp/switcch/internal/profile"
)

// Indirected for tests.
var (
	readBackupCred    = func(slot int) (string, error) { return keychain.Read(account.CredService(slot)) }
	writeBackupCred   = func(slot int, cred string) error { return keychain.Write(account.CredService(slot), cred) }
	refresh           = oauth.Refresh
	withRefreshLock   = oauth.WithRefreshLock
	seed              = profile.Seed
	syncShared        = profile.SyncShared
	profileDirFn      = profile.Dir
	defaultClaudeHome = func() string { home, _ := os.UserHomeDir(); return filepath.Join(home, ".claude") }
)

// Prepare readies account a's isolated profile and returns its CLAUDE_CONFIG_DIR
// and the environment to launch Claude Code with. It refreshes the credential if
// expired (persisting the rotated blob), seeds the profile, and syncs shared config.
func Prepare(a account.Account) (string, []string, error) {
	cred, err := readBackupCred(a.Slot)
	if err != nil {
		return "", nil, fmt.Errorf("launch: load credential for slot %d: %w", a.Slot, err)
	}
	blob, err := credblob.Parse([]byte(cred))
	if err != nil {
		return "", nil, fmt.Errorf("launch: parse credential: %w", err)
	}
	dir := profileDirFn(a)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	if blob.IsExpired(time.Now()) {
		if err := withRefreshLock(dir, func() error {
			tr, err := refresh(context.Background(), blob.RefreshToken())
			if err != nil {
				return err
			}
			blob.ApplyRefresh(tr.AccessToken, tr.RefreshToken, tr.ExpiresIn, tr.Scopes)
			nb, err := blob.Marshal()
			if err != nil {
				return err
			}
			cred = string(nb)
			return writeBackupCred(a.Slot, cred)
		}); err != nil {
			return "", nil, fmt.Errorf("launch: refresh slot %d: %w", a.Slot, err)
		}
	}
	if err := seed(a, cred); err != nil {
		return "", nil, err
	}
	if err := syncShared(dir, defaultClaudeHome()); err != nil {
		return "", nil, err
	}
	env := append([]string{"CLAUDE_CONFIG_DIR=" + dir}, profile.ScrubEnv(os.Environ())...)
	return dir, env, nil
}

// Exec replaces the current process with `claude` running under the prepared env.
func Exec(configDir string, env, claudeArgs []string) error {
	_ = configDir // carried for clarity; CLAUDE_CONFIG_DIR is already in env
	path, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("launch: claude not found in PATH: %w", err)
	}
	return syscall.Exec(path, append([]string{"claude"}, claudeArgs...), env)
}
