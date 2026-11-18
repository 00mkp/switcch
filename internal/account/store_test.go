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

func TestFallbackPoolAndPin(t *testing.T) {
	s := newTempStore(t)
	p, _ := s.Add(Account{Email: "a@x.com", Alias: "personal"})
	w, _ := s.Add(Account{Email: "b@y.com", Alias: "work"})

	if err := s.SetFallback("work", []string{"personal"}); err != nil {
		t.Fatal(err)
	}
	wa, _ := s.Resolve("work")
	pool := s.Pool(wa)
	if len(pool) != 2 || pool[0].Slot != w.Slot || pool[1].Slot != p.Slot {
		t.Fatalf("work pool = %+v, want [work personal]", pool)
	}
	pa, _ := s.Resolve("personal")
	if pp := s.Pool(pa); len(pp) != 1 || pp[0].Slot != p.Slot {
		t.Fatalf("personal must be pinned, pool = %+v", pp)
	}
	// No transitive chaining: personal's own fallback does not extend work's pool.
	o, _ := s.Add(Account{Email: "c@z.com", Alias: "other"})
	if err := s.SetFallback("personal", []string{"other"}); err != nil {
		t.Fatal(err)
	}
	wa, _ = s.Resolve("work")
	if pool := s.Pool(wa); len(pool) != 2 {
		t.Fatalf("pool must not chain transitively, got %+v (other=%d)", pool, o.Slot)
	}
}

func TestFallbackValidation(t *testing.T) {
	s := newTempStore(t)
	s.Add(Account{Email: "a@x.com", Alias: "personal"})
	s.Add(Account{Email: "b@y.com", Alias: "work"})
	if err := s.SetFallback("work", []string{"work"}); err == nil {
		t.Fatal("self-reference must error")
	}
	if err := s.SetFallback("work", []string{"nope"}); err == nil {
		t.Fatal("unknown account must error")
	}
	if err := s.SetFallback("work", nil); err != nil { // clear
		t.Fatal(err)
	}
}

func TestRemoveStripsFromFallbacks(t *testing.T) {
	s := newTempStore(t)
	s.Add(Account{Email: "a@x.com", Alias: "personal"})
	s.Add(Account{Email: "b@y.com", Alias: "work"})
	_ = s.SetFallback("work", []string{"personal"})
	if err := s.Remove("personal"); err != nil {
		t.Fatal(err)
	}
	wa, _ := s.Resolve("work")
	if len(wa.Fallback) != 0 {
		t.Fatalf("removed account still in fallback: %v", wa.Fallback)
	}
}

func TestFallbackSurvivesAliasRenameAndReload(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s, _ := Load()
	s.Add(Account{Email: "a@x.com", Alias: "personal"})
	s.Add(Account{Email: "b@y.com", Alias: "work"})
	_ = s.SetFallback("work", []string{"personal"})
	for i := range s.Accounts { // rename personal -> home
		if s.Accounts[i].Alias == "personal" {
			s.Accounts[i].Alias = "home"
		}
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s2, _ := Load()
	wa, _ := s2.Resolve("work")
	pool := s2.Pool(wa)
	if len(pool) != 2 || pool[1].Alias != "home" {
		t.Fatalf("fallback should follow the slot across rename+reload, pool = %+v", pool)
	}
}
