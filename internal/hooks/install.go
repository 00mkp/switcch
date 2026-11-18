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
)

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
	mode := os.FileMode(0o644)
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if !add {
			return nil
		}
		b = []byte("{}")
	case err != nil:
		return err
	default:
		if fi, err := os.Stat(path); err == nil {
			mode = fi.Mode().Perm()
		}
	}

	top, err := parseObject(b)
	if err != nil {
		return fmt.Errorf("hooks: %s is not a JSON object: %w", path, err)
	}
	hooksRaw := get(top, "hooks")
	if hooksRaw == nil {
		hooksRaw = json.RawMessage("{}")
	}
	hk, err := parseObject(hooksRaw)
	if err != nil {
		return fmt.Errorf("hooks: %s has a malformed \"hooks\" section: %w", path, err)
	}
	for _, ev := range Events {
		var groups []json.RawMessage
		if raw := get(hk, ev); raw != nil {
			if err := json.Unmarshal(raw, &groups); err != nil {
				return fmt.Errorf("hooks: malformed %s hooks: %w", ev, err)
			}
		}
		var kept []json.RawMessage
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
		v, err := json.Marshal(kept)
		if err != nil {
			return err
		}
		hk = set(hk, ev, v)
	}
	if len(hk) == 0 {
		top = del(top, "hooks")
	} else {
		top = set(top, "hooks", emitObject(hk))
	}

	var out bytes.Buffer
	if err := json.Indent(&out, emitObject(top), "", "  "); err != nil {
		return err
	}
	out.WriteByte('\n')
	if bytes.Equal(out.Bytes(), b) {
		return nil // already up to date: don't touch a file Claude Code watches
	}
	tmp := path + ".switcch.tmp"
	if err := os.WriteFile(tmp, out.Bytes(), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
