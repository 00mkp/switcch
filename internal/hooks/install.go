// Package hooks installs switcch's Claude Code hooks and handles them when they fire.
package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Seams: where the edit lock lives (NOT beside settings.json, which is often in
// a git-tracked dir) and a hook run just before the atomic rename (tests).
var (
	lockPath     = defaultLockPath
	beforeRename = func() {}
)

func defaultLockPath() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "switcch", "settings.lock")
}

// Events are the Claude Code hook events switcch listens to.
var Events = []string{"UserPromptSubmit", "Stop", "StopFailure"}

// field is one key/value of a JSON object, kept in source order so editing the
// user's (often git-tracked) settings.json doesn't reshuffle unrelated keys.
type field struct {
	key string
	val json.RawMessage
}

func parseObject(b []byte) ([]field, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not a JSON object")
	}
	var out []field
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, ok := t.(string)
		if !ok {
			return nil, errors.New("bad object key")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		out = append(out, field{k, raw})
	}
	return out, nil
}

func emitObject(fs []field) []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range fs {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(f.key)
		b.Write(k)
		b.WriteByte(':')
		b.Write(f.val)
	}
	b.WriteByte('}')
	return b.Bytes()
}

func get(fs []field, k string) json.RawMessage {
	for _, f := range fs {
		if f.key == k {
			return f.val
		}
	}
	return nil
}

func set(fs []field, k string, v json.RawMessage) []field {
	for i := range fs {
		if fs[i].key == k {
			fs[i].val = v
			return fs
		}
	}
	return append(fs, field{k, v})
}

func del(fs []field, k string) []field {
	out := fs[:0]
	for _, f := range fs {
		if f.key != k {
			out = append(out, f)
		}
	}
	return out
}

// isOurs reports whether a hook matcher-group was installed by switcch.
func isOurs(group json.RawMessage) bool {
	var g struct {
		Hooks []struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if json.Unmarshal(group, &g) != nil {
		return false
	}
	for _, h := range g.Hooks {
		if strings.Contains(h.Command, "switcch") && strings.Contains(h.Command, "__hook ") {
			return true
		}
	}
	return false
}

func ourGroup(exe, event string) json.RawMessage {
	quoted := "'" + strings.ReplaceAll(exe, "'", `'\''`) + "'"
	g := map[string]any{
		"hooks": []map[string]any{{
			"type":    "command",
			"command": quoted + " __hook " + event,
			"timeout": 5,
		}},
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(g)
	return json.RawMessage(bytes.TrimSpace(b.Bytes()))
}

// Install adds switcch's hooks to settingsPath (idempotent; replaces any older
// switcch entries). Unrelated keys, hooks and key order are preserved.
func Install(settingsPath, exe string) error { return edit(settingsPath, exe, true) }

// Uninstall removes exactly the hooks switcch installed.
func Uninstall(settingsPath string) error { return edit(settingsPath, "", false) }

func edit(settingsPath, exe string, add bool) error {
	path := settingsPath
	if target, err := filepath.EvalSymlinks(settingsPath); err == nil {
		path = target // write through a symlinked settings.json, never replace the link
	}
	unlock, err := lockFile(lockPath())
	if err != nil {
		return err
	}
	defer unlock()
	for range 3 {
		mode := os.FileMode(0o644)
		b, err := os.ReadFile(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if !add {
				return nil
			}
			b = nil
		case err != nil:
			return err
		default:
			if fi, err := os.Stat(path); err == nil {
				mode = fi.Mode().Perm()
			}
		}
		out, err := render(b, path, exe, add)
		if err != nil {
			return err
		}
		if bytes.Equal(out, b) {
			return nil // already up to date: don't touch a file Claude Code watches
		}
		tmp, err := os.CreateTemp(filepath.Dir(path), ".settings.json.switcch-*")
		if err != nil {
			return err
		}
		_, werr := tmp.Write(out)
		cerr := tmp.Close()
		if werr == nil {
			werr = cerr
		}
		if werr == nil {
			werr = os.Chmod(tmp.Name(), mode)
		}
		if werr != nil {
			_ = os.Remove(tmp.Name())
			return werr
		}
		beforeRename()
		// Claude Code doesn't take our lock: if it saved settings since we
		// read them, redo the edit on its version instead of clobbering it.
		if now, err := os.ReadFile(path); (err == nil && !bytes.Equal(now, b)) || (err != nil && b != nil) {
			_ = os.Remove(tmp.Name())
			continue
		}
		return os.Rename(tmp.Name(), path)
	}
	return fmt.Errorf("hooks: %s kept changing while switcch edited it; try again", path)
}

func lockFile(p string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// render returns settings b (nil = no file) with switcch's hooks added or
// removed, preserving every other key, hook and the key order.
func render(b []byte, path, exe string, add bool) ([]byte, error) {
	if b == nil {
		b = []byte("{}")
	}
	top, err := parseObject(b)
	if err != nil {
		return nil, fmt.Errorf("hooks: %s is not a JSON object: %w", path, err)
	}
	hooksRaw := get(top, "hooks")
	if hooksRaw == nil {
		hooksRaw = json.RawMessage("{}")
	}
	hk, err := parseObject(hooksRaw)
	if err != nil {
		return nil, fmt.Errorf("hooks: %s has a malformed \"hooks\" section: %w", path, err)
	}
	for _, ev := range Events {
		var groups []json.RawMessage
		if raw := get(hk, ev); raw != nil {
			if err := json.Unmarshal(raw, &groups); err != nil {
				return nil, fmt.Errorf("hooks: malformed %s hooks: %w", ev, err)
			}
		}
		var kept [][]byte
		for _, g := range groups {
			if !isOurs(g) {
				kept = append(kept, g)
			}
		}
		if add {
			kept = append(kept, ourGroup(exe, ev))
		}
		if len(kept) == 0 {
			hk = del(hk, ev)
			continue
		}
		// Join the raw groups by hand: json.Marshal would HTML-escape the
		// user's own hook commands (& < > → \u0026 ...).
		hk = set(hk, ev, append(append([]byte("["), bytes.Join(kept, []byte(","))...), ']'))
	}
	if len(hk) == 0 {
		top = del(top, "hooks")
	} else {
		top = set(top, "hooks", emitObject(hk))
	}
	var out bytes.Buffer
	if err := json.Indent(&out, emitObject(top), "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}
