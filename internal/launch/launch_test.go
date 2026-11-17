//go:build darwin

package launch

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/00mkp/switcch/internal/account"
	"github.com/00mkp/switcch/internal/oauth"
	"github.com/00mkp/switcch/internal/session"
)

func TestPrepareRecordsSession(t *testing.T) {
	saveRestore(t)
	origRec := record
	t.Cleanup(func() { record = origRec })

	acc := account.Account{Slot: 3, Email: "a@x.com"}
	valid := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"OK","refreshToken":"RT","expiresAt":%d}}`, time.Now().Add(time.Hour).UnixMilli())
	currentCred = func(account.Account) (string, error) { return valid, nil }
	writeBackupCred = func(int, string) error { return nil }
	refresh = func(context.Context, string) (oauth.TokenResponse, error) { return oauth.TokenResponse{}, nil }
	withRefreshLock = func(string, func() error) error { return nil }
	seed = func(account.Account, string) error { return nil }
	syncShared = func(string, string) error { return nil }
	profileDirFn = func(account.Account) string { return "/tmp/p" }
	defaultClaudeHome = func() string { return "/h/.claude" }

	var recorded session.Session
	record = func(s session.Session) error { recorded = s; return nil }

	if _, _, err := Prepare(acc); err != nil {
		t.Fatal(err)
	}
	if recorded.Slot != 3 {
		t.Fatalf("recorded slot = %d, want 3", recorded.Slot)
	}
	if recorded.PID != os.Getpid() {
		t.Fatalf("recorded pid = %d, want current pid %d", recorded.PID, os.Getpid())
	}
}

func saveRestore(t *testing.T) {
	t.Helper()
	a, b, c, d := currentCred, writeBackupCred, refresh, withRefreshLock
	e, f, g, h := seed, syncShared, profileDirFn, defaultClaudeHome
	t.Cleanup(func() {
		currentCred, writeBackupCred, refresh, withRefreshLock = a, b, c, d
		seed, syncShared, profileDirFn, defaultClaudeHome = e, f, g, h
	})
}

func TestPrepareRefreshesExpiredAndBuildsEnv(t *testing.T) {
	saveRestore(t)
	t.Setenv("ANTHROPIC_API_KEY", "leak")
	acc := account.Account{Slot: 1, Email: "a@x.com"}

	currentCred = func(account.Account) (string, error) {
		return `{"claudeAiOauth":{"accessToken":"OLD","refreshToken":"RT","expiresAt":1}}`, nil // expiresAt=1ms => expired
	}
	var persisted string
	writeBackupCred = func(slot int, cred string) error { persisted = cred; return nil }
	refreshCalls := 0
	refresh = func(ctx context.Context, rt string) (oauth.TokenResponse, error) {
		refreshCalls++
		if rt != "RT" {
			t.Fatalf("refresh with rt=%q, want RT", rt)
		}
		return oauth.TokenResponse{AccessToken: "NEW", RefreshToken: "RT2", ExpiresIn: 3600}, nil
	}
	withRefreshLock = func(dir string, fn func() error) error { return fn() }
	var seeded string
	seed = func(a account.Account, cred string) error { seeded = cred; return nil }
	syncShared = func(profileDir, home string) error { return nil }
	profileDirFn = func(a account.Account) string { return "/tmp/profiles/a" }
	defaultClaudeHome = func() string { return "/home/x/.claude" }

	dir, env, err := Prepare(acc)
	if err != nil {
		t.Fatal(err)
	}
	if refreshCalls != 1 {
		t.Fatalf("refresh called %d times, want 1", refreshCalls)
	}
	if dir != "/tmp/profiles/a" {
		t.Fatalf("dir = %q", dir)
	}
	if !strings.Contains(persisted, "NEW") || !strings.Contains(seeded, "NEW") {
		t.Fatalf("expected rotated token persisted+seeded; persisted=%s seeded=%s", persisted, seeded)
	}
	if !slices.Contains(env, "CLAUDE_CONFIG_DIR=/tmp/profiles/a") {
		t.Fatalf("env missing CLAUDE_CONFIG_DIR: %v", env)
	}
	for _, e := range env {
		if strings.HasPrefix(e, "ANTHROPIC_API_KEY") {
			t.Fatalf("auth-override var not scrubbed from launch env: %q", e)
		}
	}
}

// If another holder refreshes the account while we wait for the refresh lock,
// Prepare must re-read inside the lock and NOT refresh again with the now-spent
// pre-lock token (which would double-spend and kill a healthy account).
func TestPrepareReReadsInsideLockAvoidsDoubleSpend(t *testing.T) {
	saveRestore(t)
	acc := account.Account{Slot: 1, Email: "a@x.com"}
	expired := `{"claudeAiOauth":{"accessToken":"OLD","refreshToken":"RT0","expiresAt":1}}`
	fresh := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"NEW","refreshToken":"RT1","expiresAt":%d}}`, time.Now().Add(time.Hour).UnixMilli())
	calls := 0
	currentCred = func(account.Account) (string, error) {
		calls++
		if calls == 1 {
			return expired, nil // pre-lock read sees it expired
		}
		return fresh, nil // inside the lock another holder already refreshed it
	}
	writeBackupCred = func(slot int, cred string) error { return nil }
	refreshCalls := 0
	refresh = func(ctx context.Context, rt string) (oauth.TokenResponse, error) {
		refreshCalls++
		return oauth.TokenResponse{}, nil
	}
	withRefreshLock = func(dir string, fn func() error) error { return fn() }
	var seeded string
	seed = func(a account.Account, cred string) error { seeded = cred; return nil }
	syncShared = func(p, h string) error { return nil }
	profileDirFn = func(a account.Account) string { return "/tmp/p" }
	defaultClaudeHome = func() string { return "/h/.claude" }

	if _, _, err := Prepare(acc); err != nil {
		t.Fatal(err)
	}
	if refreshCalls != 0 {
		t.Fatalf("token was already refreshed by another holder; must not refresh again (calls=%d)", refreshCalls)
	}
	if !strings.Contains(seeded, "NEW") {
		t.Fatalf("must seed the re-read fresh credential; seeded=%s", seeded)
	}
}

func TestPrepareValidTokenSkipsRefresh(t *testing.T) {
	saveRestore(t)
	acc := account.Account{Slot: 1, Email: "a@x.com"}
	valid := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"OK","refreshToken":"RT","expiresAt":%d}}`, time.Now().Add(time.Hour).UnixMilli())
	currentCred = func(account.Account) (string, error) { return valid, nil }
	writeBackupCred = func(slot int, cred string) error { return nil }
	refreshCalls := 0
	refresh = func(ctx context.Context, rt string) (oauth.TokenResponse, error) {
		refreshCalls++
		return oauth.TokenResponse{}, nil
	}
	withRefreshLock = func(dir string, fn func() error) error { return fn() }
	seed = func(a account.Account, cred string) error { return nil }
	syncShared = func(p, h string) error { return nil }
	profileDirFn = func(a account.Account) string { return "/tmp/p" }
	defaultClaudeHome = func() string { return "/h/.claude" }

	if _, _, err := Prepare(acc); err != nil {
		t.Fatal(err)
	}
	if refreshCalls != 0 {
		t.Fatalf("valid token must not trigger refresh; calls=%d", refreshCalls)
	}
}

func TestPrepareCapturesBeforeSeeding(t *testing.T) {
	saveRestore(t)
	origRec := record
	t.Cleanup(func() { record = origRec })
	acc := account.Account{Slot: 1, Email: "a@x.com"}
	valid := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"FRESH","refreshToken":"RT","expiresAt":%d}}`, time.Now().Add(time.Hour).UnixMilli())
	captured := false
	currentCred = func(account.Account) (string, error) { captured = true; return valid, nil }
	writeBackupCred = func(int, string) error { return nil }
	refresh = func(context.Context, string) (oauth.TokenResponse, error) { return oauth.TokenResponse{}, nil }
	withRefreshLock = func(string, func() error) error { return nil }
	var seededCred string
	seed = func(a account.Account, cred string) error {
		if !captured {
			t.Fatal("seed ran before capture-back; Claude's rotation would be lost")
		}
		seededCred = cred
		return nil
	}
	syncShared = func(string, string) error { return nil }
	profileDirFn = func(account.Account) string { return "/tmp/p" }
	defaultClaudeHome = func() string { return "/h" }
	record = func(session.Session) error { return nil }
	if _, _, err := Prepare(acc); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seededCred, "FRESH") {
		t.Fatalf("seeded %q, want the captured Current credential", seededCred)
	}
}
