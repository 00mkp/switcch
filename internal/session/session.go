// Package session is the registry of running switcch-launched Claude Code
// sessions: which account each is on, recorded at launch and pruned by liveness.
package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Session is one supervised chat. PID is the switcch supervisor's pid; Slot is
// the account it is on right now; Pool its configured failover order (as
// launched; unchanged by switches);
// SessionID the Claude conversation id once a hook has reported it.
type Session struct {
	Slot      int       `json:"slot"`
	PID       int       `json:"pid"`
	Cwd       string    `json:"cwd"`
	Started   time.Time `json:"started"`
	SessionID string    `json:"sessionId,omitempty"`
	Pool      []int     `json:"pool,omitempty"`
}

// aliveFn reports whether a pid is a live process (test seam).
var aliveFn = defaultAlive

func defaultAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func dataHome() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "switcch")
}

func regPath() string { return filepath.Join(dataHome(), "sessions.json") }

// Record appends a session (replacing any existing record with the same pid).
func Record(s Session) error {
	unlock, err := lock()
	if err != nil {
		return err
	}
	defer unlock()
	list := load()
	out := list[:0]
	for _, x := range list {
		if x.PID != s.PID {
			out = append(out, x)
		}
	}
	out = append(out, s)
	return save(out)
}

// Update applies fn to the recorded session with this pid (no-op if absent).
func Update(pid int, fn func(*Session)) error {
	unlock, err := lock()
	if err != nil {
		return err
	}
	defer unlock()
	list := load()
	for i := range list {
		if list[i].PID == pid {
			fn(&list[i])
			return save(list)
		}
	}
	return nil
}

// List returns the live sessions, pruning dead ones from the registry file.
func List() ([]Session, error) {
	unlock, err := lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	list := load()
	live := make([]Session, 0, len(list))
	for _, s := range list {
		if aliveFn(s.PID) {
			live = append(live, s)
		}
	}
	if len(live) != len(list) {
		_ = save(live)
	}
	return live, nil
}

func load() []Session {
	b, err := os.ReadFile(regPath())
	if err != nil {
		return nil
	}
	var out []Session
	if json.Unmarshal(b, &out) != nil {
		return nil
	}
	return out
}

func save(list []Session) error {
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataHome(), 0o700); err != nil {
		return err
	}
	tmp := regPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, regPath())
}

func lock() (func(), error) {
	lp := filepath.Join(dataHome(), ".sessions.lock")
	_ = os.MkdirAll(dataHome(), 0o700)
	for i := 0; i < 100; i++ {
		if err := os.Mkdir(lp, 0o700); err == nil {
			return func() { _ = os.Remove(lp) }, nil
		}
		if fi, e := os.Stat(lp); e == nil && time.Since(fi.ModTime()) > 30*time.Second {
			_ = os.Remove(lp)
			continue
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil, errors.New("session: registry lock timeout")
}
