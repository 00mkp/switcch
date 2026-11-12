package account

import (
	"errors"
	"testing"
)

func newTempStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAddAssignsSlots(t *testing.T) {
	s := newTempStore(t)
	a, err := s.Add(Account{Email: "a@x.com", Alias: "personal"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Slot != 1 {
		t.Fatalf("first slot = %d, want 1", a.Slot)
	}
	b, _ := s.Add(Account{Email: "b@y.com", Alias: "work"})
	if b.Slot != 2 {
		t.Fatalf("second slot = %d, want 2", b.Slot)
	}
}

func TestResolve(t *testing.T) {
	s := newTempStore(t)
	s.Add(Account{Email: "a@x.com", Alias: "personal"})
	w, _ := s.Add(Account{Email: "b@y.com", Alias: "work"})
	if got, err := s.Resolve("2"); err != nil || got.Email != "b@y.com" {
		t.Fatalf("Resolve by slot: got %+v err %v", got, err)
	}
	if got, err := s.Resolve("b@y.com"); err != nil || got.Slot != 2 {
		t.Fatalf("Resolve by email: got %+v err %v", got, err)
	}
	if got, err := s.Resolve("work"); err != nil || got.Slot != w.Slot {
		t.Fatalf("Resolve by alias: got %+v err %v", got, err)
	}
	if _, err := s.Resolve("nope"); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("Resolve unknown: err = %v, want ErrNoAccount", err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s, _ := Load()
	s.Add(Account{Email: "a@x.com", Alias: "personal", OrgUUID: "o1", DisplayName: "A"})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s2, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.List()) != 1 {
		t.Fatalf("after reload: %d accounts, want 1", len(s2.List()))
	}
	got, _ := s2.Resolve("personal")
	if got.Email != "a@x.com" || got.DisplayName != "A" {
		t.Fatalf("reloaded account = %+v", got)
	}
}

func TestRemove(t *testing.T) {
	s := newTempStore(t)
	s.Add(Account{Email: "a@x.com", Alias: "personal"})
	s.Add(Account{Email: "b@y.com", Alias: "work"})
	if err := s.Remove("personal"); err != nil {
		t.Fatal(err)
	}
	if len(s.List()) != 1 || s.List()[0].Email != "b@y.com" {
		t.Fatalf("after remove: %+v", s.List())
	}
	if err := s.Remove("nope"); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("remove unknown: err = %v, want ErrNoAccount", err)
	}
}

func TestCredServiceDistinctPerSlot(t *testing.T) {
	if CredService(1) == CredService(2) {
		t.Fatal("CredService must differ per slot")
	}
}
