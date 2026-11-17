//go:build darwin

package credsync

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/00mkp/switcch/internal/account"
	"github.com/00mkp/switcch/internal/credfile"
	"github.com/00mkp/switcch/internal/keychain"
)

func credJSON(access, refresh string, expMs int64) string {
	return fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":%q,"expiresAt":%d}}`, access, refresh, expMs)
}

func stubKeychain(t *testing.T, fn func(account.Account) (string, error)) {
	t.Helper()
	orig := readProfileKeychain
	readProfileKeychain = fn
	t.Cleanup(func() { readProfileKeychain = orig })
}

func TestCurrentPrefersNewerKeychain(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	a := account.Account{Slot: 1, Email: "a@x.com"}
	if err := credfile.Write(a.Slot, credJSON("OLD", "RT", time.Now().Add(time.Hour).UnixMilli())); err != nil {
		t.Fatal(err)
	}
	newer := credJSON("NEW", "RT2", time.Now().Add(2*time.Hour).UnixMilli())
	stubKeychain(t, func(account.Account) (string, error) { return newer, nil })

	got, err := Current(a)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "NEW") {
		t.Fatalf("Current = %s, want the newer keychain token", got)
	}
	fc, _ := credfile.Read(a.Slot)
	if !strings.Contains(fc, "NEW") {
		t.Fatal("newer keychain token was not synced back into the file backup")
	}
}

func TestCurrentFallsBackToFileWhenNoKeychain(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	a := account.Account{Slot: 1, Email: "a@x.com"}
	if err := credfile.Write(a.Slot, credJSON("SEED", "RT", time.Now().Add(time.Hour).UnixMilli())); err != nil {
		t.Fatal(err)
	}
	stubKeychain(t, func(account.Account) (string, error) { return "", keychain.ErrNotFound })

	got, err := Current(a)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "SEED") {
		t.Fatalf("Current = %s, want the file seed", got)
	}
}

func TestCurrentKeepsFileWhenKeychainOlder(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	a := account.Account{Slot: 1, Email: "a@x.com"}
	if err := credfile.Write(a.Slot, credJSON("FILE", "RT", time.Now().Add(2*time.Hour).UnixMilli())); err != nil {
		t.Fatal(err)
	}
	older := credJSON("KC", "RT", time.Now().Add(time.Hour).UnixMilli())
	stubKeychain(t, func(account.Account) (string, error) { return older, nil })

	got, err := Current(a)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "FILE") {
		t.Fatalf("Current = %s, want the newer file token", got)
	}
}

func TestSyncBackupIgnoresGarbage(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	a := account.Account{Slot: 1}
	good := credJSON("GOOD", "RT", time.Now().Add(time.Hour).UnixMilli())
	if err := credfile.Write(a.Slot, good); err != nil {
		t.Fatal(err)
	}
	if err := SyncBackup(a, "not json"); err != nil {
		t.Fatal(err)
	}
	fc, _ := credfile.Read(a.Slot)
	if !strings.Contains(fc, "GOOD") {
		t.Fatal("garbage must not overwrite a good backup")
	}
}
