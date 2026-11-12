//go:build darwin

package launch

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/00mkp/switcch/internal/account"
	"github.com/00mkp/switcch/internal/oauth"
)

func saveRestore(t *testing.T) {
	t.Helper()
	a, b, c, d := readBackupCred, writeBackupCred, refresh, withRefreshLock
	e, f, g, h := seed, syncShared, profileDirFn, defaultClaudeHome
	t.Cleanup(func() {
		readBackupCred, writeBackupCred, refresh, withRefreshLock = a, b, c, d
		seed, syncShared, profileDirFn, defaultClaudeHome = e, f, g, h
	})
}

func TestPrepareRefreshesExpiredAndBuildsEnv(t *testing.T) {
	saveRestore(t)
	t.Setenv("ANTHROPIC_API_KEY", "leak")
	acc := account.Account{Slot: 1, Email: "a@x.com"}

	readBackupCred = func(slot int) (string, error) {
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

func TestPrepareValidTokenSkipsRefresh(t *testing.T) {
	saveRestore(t)
	acc := account.Account{Slot: 1, Email: "a@x.com"}
	valid := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"OK","refreshToken":"RT","expiresAt":%d}}`, time.Now().Add(time.Hour).UnixMilli())
	readBackupCred = func(slot int) (string, error) { return valid, nil }
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
