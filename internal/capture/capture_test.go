//go:build darwin

package capture

import (
	"errors"
	"testing"

	"github.com/00mkp/switcch/internal/account"
	"github.com/00mkp/switcch/internal/keychain"
)

func saveRestore(t *testing.T) {
	t.Helper()
	oc, oi, ow := readLiveCred, readLiveIdentity, writeBackupCred
	t.Cleanup(func() { readLiveCred, readLiveIdentity, writeBackupCred = oc, oi, ow })
}

func TestCaptureAddsAccountAndStoresCred(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store, _ := account.Load()
	saveRestore(t)

	readLiveCred = func() (string, error) { return `{"claudeAiOauth":{"accessToken":"AT"}}`, nil }
	readLiveIdentity = func() (account.Account, error) {
		return account.Account{Email: "a@x.com", OrgUUID: "org1", DisplayName: "A"}, nil
	}
	var storedSlot int
	var storedCred string
	writeBackupCred = func(slot int, cred string) error {
		storedSlot, storedCred = slot, cred
		return nil
	}

	a, err := Capture(store, "personal")
	if err != nil {
		t.Fatal(err)
	}
	if a.Email != "a@x.com" || a.OrgUUID != "org1" || a.Alias != "personal" || a.Slot != 1 {
		t.Fatalf("captured account = %+v", a)
	}
	if storedSlot != 1 || storedCred != `{"claudeAiOauth":{"accessToken":"AT"}}` {
		t.Fatalf("stored cred: slot=%d cred=%s", storedSlot, storedCred)
	}
	if len(store.List()) != 1 {
		t.Fatalf("store has %d accounts, want 1", len(store.List()))
	}
}

func TestCaptureNotLoggedIn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store, _ := account.Load()
	saveRestore(t)

	readLiveCred = func() (string, error) { return "", keychain.ErrNotFound }

	if _, err := Capture(store, ""); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("err = %v, want ErrNotLoggedIn", err)
	}
	if len(store.List()) != 0 {
		t.Fatal("no account should be added when not logged in")
	}
}
