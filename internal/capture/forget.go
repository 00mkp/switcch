//go:build darwin

package capture

import (
	"fmt"
	"os"

	"github.com/00mkp/switcch/internal/account"
	"github.com/00mkp/switcch/internal/credfile"
	"github.com/00mkp/switcch/internal/profile"
)

// Indirected for tests.
var (
	forgetDelCred   = credfile.Delete
	forgetRmProfile = func(a account.Account) error { return os.RemoveAll(profile.Dir(a)) }
)

// Forget removes an account: it deletes the stored backup credential, removes
// the profile directory (with its seeded .credentials.json), and drops the
// identity from the registry. Caller persists the store.
func Forget(store *account.Store, ref string) (account.Account, error) {
	a, err := store.Resolve(ref)
	if err != nil {
		return account.Account{}, err
	}
	if err := forgetDelCred(a.Slot); err != nil {
		return account.Account{}, fmt.Errorf("capture: delete credential for slot %d: %w", a.Slot, err)
	}
	if err := forgetRmProfile(a); err != nil {
		return account.Account{}, fmt.Errorf("capture: remove profile dir: %w", err)
	}
	if err := store.Remove(ref); err != nil {
		return account.Account{}, err
	}
	return a, nil
}
