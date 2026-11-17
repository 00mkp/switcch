//go:build darwin

package credsync

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/00mkp/switcch/internal/account"
	"github.com/00mkp/switcch/internal/credfile"
)

func TestRecoverMatchingIdentitySyncs(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	a := account.Account{Slot: 1, Email: "a@x.com"}
	o1, o2 := readLiveIdentityEmail, readLiveDefaultCred
	t.Cleanup(func() { readLiveIdentityEmail, readLiveDefaultCred = o1, o2 })
	readLiveIdentityEmail = func() (string, error) { return "a@x.com", nil }
	live := credJSON("LIVE", "RT", time.Now().Add(time.Hour).UnixMilli())
	readLiveDefaultCred = func() (string, error) { return live, nil }

	got, err := Recover(a)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "LIVE") {
		t.Fatalf("Recover = %s, want the live credential", got)
	}
	fc, _ := credfile.Read(a.Slot)
	if !strings.Contains(fc, "LIVE") {
		t.Fatal("recovered credential was not synced into the backup")
	}
}

func TestRecoverMismatchRefuses(t *testing.T) {
	a := account.Account{Slot: 1, Email: "a@x.com"}
	o1, o2 := readLiveIdentityEmail, readLiveDefaultCred
	t.Cleanup(func() { readLiveIdentityEmail, readLiveDefaultCred = o1, o2 })
	readLiveIdentityEmail = func() (string, error) { return "other@y.com", nil }
	readLiveDefaultCred = func() (string, error) {
		t.Fatal("must not read the live credential on an identity mismatch (isolation)")
		return "", nil
	}

	if _, err := Recover(a); !errors.Is(err, ErrCannotRecover) {
		t.Fatalf("err = %v, want ErrCannotRecover", err)
	}
}
