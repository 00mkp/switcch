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

func TestCaptureRejectsEmptyIdentity(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store, _ := account.Load()
	saveRestore(t)

	readLiveCred = func() (string, error) { return `{"claudeAiOauth":{"accessToken":"AT"}}`, nil }
	readLiveIdentity = func() (account.Account, error) { return account.Account{}, nil } // no email
	writeBackupCred = func(slot int, cred string) error {
		t.Fatal("must not store a credential for an empty identity")
		return nil
	}

	if _, err := Capture(store, ""); err == nil {
		t.Fatal("Capture should error when no account email can be determined")
	}
	if len(store.List()) != 0 {
		t.Fatalf("must not register an empty-identity account; store has %d", len(store.List()))
	}
}

func TestForgetDeletesCredAndProfile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store, _ := account.Load()
	a, _ := store.Add(account.Account{Email: "a@x.com", Alias: "work"})

	oc, orp := forgetDelCred, forgetRmProfile
	t.Cleanup(func() { forgetDelCred, forgetRmProfile = oc, orp })
	var delSlot int
	rmCalled := false
	forgetDelCred = func(slot int) error { delSlot = slot; return nil }
	forgetRmProfile = func(account.Account) error { rmCalled = true; return nil }

	got, err := Forget(store, "work")
	if err != nil {
		t.Fatal(err)
	}
	if got.Slot != a.Slot {
		t.Fatalf("forgot %+v, want slot %d", got, a.Slot)
	}
	if delSlot != a.Slot {
		t.Fatalf("deleted credential for slot %d, want %d", delSlot, a.Slot)
	}
	if !rmCalled {
		t.Fatal("profile directory was not removed")
	}
	if len(store.List()) != 0 {
		t.Fatal("account still present in registry after Forget")
	}
	if _, err := Forget(store, "nope"); err == nil {
		t.Fatal("Forget of unknown ref should error")
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
