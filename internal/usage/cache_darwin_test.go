//go:build darwin

package usage

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/00mkp/switcch/internal/account"
	"github.com/00mkp/switcch/internal/credfile"
	"github.com/00mkp/switcch/internal/keychain"
	"github.com/00mkp/switcch/internal/oauth"
	"github.com/00mkp/switcch/internal/profile"
)

// Exercises the REAL refresh path (profile-dir lock + credfile), which the
// stubbed cache tests never touched — the bug that hid behind a green suite.
func TestRefreshTokenRealPathLocksDirAndPersists(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	a := account.Account{Slot: 1, Email: "a@x.com"}
	if err := credfile.Write(a.Slot, `{"claudeAiOauth":{"accessToken":"OLD","refreshToken":"RT","expiresAt":1}}`); err != nil {
		t.Fatal(err)
	}
	orig := oauthRefresh
	t.Cleanup(func() { oauthRefresh = orig })
	oauthRefresh = func(ctx context.Context, rt string) (oauth.TokenResponse, error) {
		if rt != "RT" {
			t.Fatalf("refresh rt = %q, want RT", rt)
		}
		return oauth.TokenResponse{AccessToken: "NEW", RefreshToken: "RT2", ExpiresIn: 3600}, nil
	}

	tok, err := refreshToken(a, false)
	if err != nil {
		t.Fatalf("refreshToken errored (lock-path bug?): %v", err)
	}
	if tok != "NEW" {
		t.Fatalf("token = %q, want NEW", tok)
	}
	cred, err := credfile.Read(a.Slot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cred, "NEW") {
		t.Fatal("rotated token was not persisted")
	}
}

// When the profile keychain holds a fresh token (Claude rotated it), usage must
// use it and NOT refresh the stale file backup.
func TestTokenForUsesLiveProfileKeychain(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	a := account.Account{Slot: 1, Email: "a@x.com"}
	// File backup is expired — would trigger a refresh if it were the source.
	if err := credfile.Write(a.Slot, `{"claudeAiOauth":{"accessToken":"OLD","refreshToken":"RT","expiresAt":1}}`); err != nil {
		t.Fatal(err)
	}
	// Profile keychain holds a FRESH token (small enough for the keychain CLI).
	svc := keychain.ServiceName(profile.Dir(a))
	fresh := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"LIVE","refreshToken":"RT2","expiresAt":%d}}`, time.Now().Add(time.Hour).UnixMilli())
	if err := keychain.Write(svc, fresh); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = keychain.Delete(svc) })

	orig := oauthRefresh
	t.Cleanup(func() { oauthRefresh = orig })
	refreshed := false
	oauthRefresh = func(context.Context, string) (oauth.TokenResponse, error) {
		refreshed = true
		return oauth.TokenResponse{}, nil
	}

	tok, err := defaultTokenFor(a)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "LIVE" {
		t.Fatalf("token = %q, want LIVE (from the profile keychain)", tok)
	}
	if refreshed {
		t.Fatal("must not refresh when the profile keychain already has a fresh token")
	}
}

func TestRefreshSelfHealsOnInvalidGrant(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	a := account.Account{Slot: 1, Email: "a@x.com"}
	if err := credfile.Write(a.Slot, `{"claudeAiOauth":{"accessToken":"OLD","refreshToken":"RT","expiresAt":1}}`); err != nil {
		t.Fatal(err)
	}
	o1, o2 := oauthRefresh, recoverCred
	t.Cleanup(func() { oauthRefresh, recoverCred = o1, o2 })
	oauthRefresh = func(context.Context, string) (oauth.TokenResponse, error) {
		return oauth.TokenResponse{}, oauth.ErrInvalidGrant
	}
	healed := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"HEALED","refreshToken":"RT3","expiresAt":%d}}`, time.Now().Add(time.Hour).UnixMilli())
	recovered := false
	recoverCred = func(account.Account) (string, error) { recovered = true; return healed, nil }

	tok, err := refreshToken(a, false)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered {
		t.Fatal("should self-heal via recoverCred on invalid_grant")
	}
	if tok != "HEALED" {
		t.Fatalf("token = %q, want HEALED", tok)
	}
}
