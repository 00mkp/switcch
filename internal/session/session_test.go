package session

import (
	"testing"
	"time"
)

func TestRegistryRecordsAndPrunesDead(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	orig := aliveFn
	t.Cleanup(func() { aliveFn = orig })
	aliveFn = func(pid int) bool { return pid == 111 } // 111 alive, 222 dead

	if err := Record(Session{Slot: 1, PID: 111, Cwd: "/a", Started: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := Record(Session{Slot: 2, PID: 222, Cwd: "/b", Started: time.Now()}); err != nil {
		t.Fatal(err)
	}

	got, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].PID != 111 {
		t.Fatalf("List = %+v, want only PID 111", got)
	}

	// The dead entry must be pruned from the file, so a second List is still clean
	// even without re-checking liveness of a now-removed record.
	got2, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) != 1 || got2[0].Slot != 1 {
		t.Fatalf("after prune List = %+v", got2)
	}
}
