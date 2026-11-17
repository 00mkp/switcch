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
	"github.com/00mkp/switcch/internal/credfile"
	"github.com/00mkp/switcch/internal/credsync"
	"github.com/00mkp/switcch/internal/oauth"
	"github.com/00mkp/switcch/internal/profile"
	"github.com/00mkp/switcch/internal/session"
)

// Indirected for tests.
var (
	// currentCred reads the freshest credential (Claude's live profile keychain,
	// else the backup) and syncs the backup — the capture-back that keeps the
	// token from going stale. Called before seeding, which clears the keychain.
	currentCred       = credsync.Current
	writeBackupCred   = credfile.Write
	refresh           = oauth.Refresh
	withRefreshLock   = oauth.WithRefreshLock
	seed              = profile.Seed
	syncShared        = profile.SyncShared
	profileDirFn      = profile.Dir
	defaultClaudeHome = func() string { home, _ := os.UserHomeDir(); return filepath.Join(home, ".claude") }
	record            = session.Record
)

// Prepare readies account a's isolated profile and returns its CLAUDE_CONFIG_DIR
// and the environment to launch Claude Code with. It refreshes the credential if
// expired (persisting the rotated blob), seeds the profile, and syncs shared config.
func Prepare(a account.Account) (string, []string, error) {
	cred, err := currentCred(a) // capture-back: read Claude's rotated token before the seed clears it
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
			// Re-read inside the lock: another holder may have refreshed while we
			// waited, and refreshing from the pre-lock (now-spent) token would
			// double-spend the rotating refresh token and kill a healthy account.
			fresh, err := currentCred(a)
			if err != nil {
				return err
			}
			fb, err := credblob.Parse([]byte(fresh))
			if err != nil {
				return err
			}
			if !fb.IsExpired(time.Now()) {
				cred = fresh // someone else already refreshed; use theirs
				return nil
			}
			tr, err := refresh(context.Background(), fb.RefreshToken())
			if err != nil {
				return err
			}
			fb.ApplyRefresh(tr.AccessToken, tr.RefreshToken, tr.ExpiresIn, tr.Scopes)
			nb, err := fb.Marshal()
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
	// Record this session (our pid becomes `claude` after Exec). Best-effort: a
	// registry hiccup must not block launching.
	wd, _ := os.Getwd()
	_ = record(session.Session{Slot: a.Slot, PID: os.Getpid(), Cwd: wd, Started: time.Now()})
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
