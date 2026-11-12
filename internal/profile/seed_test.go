//go:build darwin

package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/00mkp/switcch/internal/account"
	"github.com/00mkp/switcch/internal/keychain"
)

func TestSeedWritesProfile(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	a := account.Account{Slot: 1, Email: "a@x.com", AccountUUID: "acc1", OrgUUID: "org1", DisplayName: "A", OrgName: "Acme"}
	dir := Dir(a)

	// Pre-create .claude.json with an unrelated key to prove the merge preserves it.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"numStartups":5}`), 0o600); err != nil {
		t.Fatal(err)
	}

	deleteCalledBeforeWrite := false
	deleteKeychain = func(service string) error {
		if _, e := os.Stat(filepath.Join(dir, ".credentials.json")); os.IsNotExist(e) {
			deleteCalledBeforeWrite = true
		}
		if service != keychain.ServiceName(dir) {
			t.Fatalf("delete service = %q, want %q", service, keychain.ServiceName(dir))
		}
		return nil
	}
	t.Cleanup(func() { deleteKeychain = defaultDeleteKeychain })

	if err := Seed(a, `{"claudeAiOauth":{"accessToken":"AT"}}`); err != nil {
		t.Fatal(err)
	}
	if !deleteCalledBeforeWrite {
		t.Fatal("stale keychain entry must be deleted before writing .credentials.json")
	}

	credPath := filepath.Join(dir, ".credentials.json")
	fi, err := os.Stat(credPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf(".credentials.json mode = %v, want 0600", fi.Mode().Perm())
	}
	if b, _ := os.ReadFile(credPath); string(b) != `{"claudeAiOauth":{"accessToken":"AT"}}` {
		t.Fatalf(".credentials.json = %s", b)
	}

	cb, _ := os.ReadFile(filepath.Join(dir, ".claude.json"))
	var m map[string]any
	if err := json.Unmarshal(cb, &m); err != nil {
		t.Fatal(err)
	}
	if m["numStartups"] == nil {
		t.Fatal("merge dropped unrelated key numStartups")
	}
	if m["hasCompletedOnboarding"] != true {
		t.Fatal("hasCompletedOnboarding not set true")
	}
	if m["theme"] != "dark" {
		t.Fatalf("theme = %v, want dark", m["theme"])
	}
	oa, ok := m["oauthAccount"].(map[string]any)
	if !ok {
		t.Fatal("oauthAccount missing")
	}
	if oa["emailAddress"] != "a@x.com" || oa["organizationUuid"] != "org1" {
		t.Fatalf("oauthAccount = %v", oa)
	}
}
