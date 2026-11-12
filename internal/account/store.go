package account

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrNoAccount is returned when no registered account matches a reference.
var ErrNoAccount = errors.New("account: no matching account")

// Store is the set of registered accounts, backed by a JSON state file.
type Store struct {
	path     string
	Accounts []Account `json:"accounts"`
}

// statePath is ${XDG_CONFIG_HOME:-~/.config}/switcch/state.json.
func statePath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "switcch", "state.json"), nil
}

// Load reads the state file, returning an empty store if it does not exist.
func Load() (*Store, error) {
	p, err := statePath()
	if err != nil {
		return nil, err
	}
	s := &Store{path: p}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("account: load: %w", err)
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("account: parse state: %w", err)
	}
	return s, nil
}

// Add registers a, assigning it the next free slot, and returns the stored copy.
func (s *Store) Add(a Account) (Account, error) {
	max := 0
	for _, x := range s.Accounts {
		if x.Slot > max {
			max = x.Slot
		}
	}
	a.Slot = max + 1
	s.Accounts = append(s.Accounts, a)
	return a, nil
}

// List returns the registered accounts.
func (s *Store) List() []Account { return s.Accounts }

// Resolve finds an account by slot number, email (case-insensitive), or alias.
func (s *Store) Resolve(ref string) (Account, error) {
	if n, err := strconv.Atoi(ref); err == nil {
		for _, a := range s.Accounts {
			if a.Slot == n {
				return a, nil
			}
		}
	}
	for _, a := range s.Accounts {
		if strings.EqualFold(a.Email, ref) || (a.Alias != "" && a.Alias == ref) {
			return a, nil
		}
	}
	return Account{}, ErrNoAccount
}

// Remove deletes the account matching ref.
func (s *Store) Remove(ref string) error {
	a, err := s.Resolve(ref)
	if err != nil {
		return err
	}
	out := s.Accounts[:0]
	for _, x := range s.Accounts {
		if x.Slot != a.Slot {
			out = append(out, x)
		}
	}
	s.Accounts = out
	return nil
}

// Save writes the state file atomically at mode 0600.
func (s *Store) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
