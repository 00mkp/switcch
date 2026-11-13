//go:build darwin

package usage

import (
	"context"
	"strings"
	"testing"

	"github.com/00mkp/switcch/internal/account"
	"github.com/00mkp/switcch/internal/credfile"
	"github.com/00mkp/switcch/internal/oauth"
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
