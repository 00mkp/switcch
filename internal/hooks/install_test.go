package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

const original = `{
  "model": "x",
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {
            "type": "command",
            "command": "echo hi"
          }
        ]
      }
    ]
  },
  "zeta": 1
}
`

func groupsFor(t *testing.T, path, event string) []map[string]any {
	t.Helper()
	b, _ := os.ReadFile(path)
	var s struct {
		Hooks map[string][]map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("settings not valid JSON: %v\n%s", err, b)
	}
	return s.Hooks[event]
}

func TestInstallIdempotentPreservesOthersAndOrder(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(p, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := Install(p, "/opt/bin/switcch"); err != nil {
			t.Fatal(err)
		}
	}
	for _, ev := range Events {
		g := groupsFor(t, p, ev)
		if len(g) != 1 {
			t.Fatalf("%s: want exactly one switcch group after 2 installs, got %d", ev, len(g))
		}
		cmd := g[0]["hooks"].([]any)[0].(map[string]any)["command"].(string)
		if !strings.Contains(cmd, "switcch") || !strings.HasSuffix(cmd, "__hook "+ev) {
			t.Fatalf("%s command = %q", ev, cmd)
		}
	}
	if pre := groupsFor(t, p, "PreToolUse"); len(pre) != 1 || pre[0]["matcher"] != "Bash" {
		t.Fatalf("unrelated PreToolUse hook disturbed: %+v", pre)
	}
	b, _ := os.ReadFile(p)
	s := string(b)
	if !(strings.Index(s, `"model"`) < strings.Index(s, `"hooks"`) && strings.Index(s, `"hooks"`) < strings.Index(s, `"zeta"`)) {
		t.Fatalf("top-level key order not preserved:\n%s", s)
	}
}

func TestUninstallRestoresOriginal(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	_ = os.WriteFile(p, []byte(original), 0o644)
	if err := Install(p, "/opt/bin/switcch"); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(p); err != nil {
		t.Fatal(err)
	}
	var want, got any
	_ = json.Unmarshal([]byte(original), &want)
	b, _ := os.ReadFile(p)
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("uninstall did not restore original:\n%s", b)
	}
}

func TestInstallFollowsSymlinkAndCreatesMissing(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.json")
	_ = os.WriteFile(real, []byte(original), 0o644)
	link := filepath.Join(dir, "settings.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := Install(link, "/opt/bin/switcch"); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlinked settings.json was replaced by a regular file")
	}
	if len(groupsFor(t, real, "Stop")) != 1 {
		t.Fatal("the symlink target was not updated")
	}

	fresh := filepath.Join(dir, "new.json")
	if err := Install(fresh, "/opt/bin/switcch"); err != nil {
		t.Fatal(err)
	}
	if len(groupsFor(t, fresh, "StopFailure")) != 1 {
		t.Fatal("missing settings file should be created with the hooks")
	}
}

func TestInstallLeavesUnrelatedHookBytesAlone(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	orig := `{
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "a > /dev/null 2>&1 && b <x>"
          }
        ]
      }
    ]
  }
}
`
	_ = os.WriteFile(p, []byte(orig), 0o644)
	if err := Install(p, "/opt/bin/switcch"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), `"a > /dev/null 2>&1 && b <x>"`) {
		t.Fatalf("unrelated hook command was re-escaped:\n%s", b)
	}
	if err := Uninstall(p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != orig {
		t.Fatalf("install+uninstall must restore the file byte-for-byte:\n%s", b)
	}
}

func isolateLock(t *testing.T) {
	t.Helper()
	lp := filepath.Join(t.TempDir(), "settings.lock")
	o := lockPath
	t.Cleanup(func() { lockPath = o })
	lockPath = func() string { return lp }
}

func TestInstallConcurrent(t *testing.T) {
	isolateLock(t)
	p := filepath.Join(t.TempDir(), "settings.json")
	_ = os.WriteFile(p, []byte(original), 0o644)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- Install(p, "/opt/bin/switcch") }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent install: %v", err)
		}
	}
	for _, ev := range Events {
		if g := groupsFor(t, p, ev); len(g) != 1 {
			t.Fatalf("%s: want one switcch group, got %d", ev, len(g))
		}
	}
	if pre := groupsFor(t, p, "PreToolUse"); len(pre) != 1 {
		t.Fatalf("unrelated hook lost: %+v", pre)
	}
}

func TestInstallRetriesWhenFileChangesUnderIt(t *testing.T) {
	isolateLock(t)
	p := filepath.Join(t.TempDir(), "settings.json")
	_ = os.WriteFile(p, []byte(original), 0o644)
	o := beforeRename
	t.Cleanup(func() { beforeRename = o })
	fired := false
	beforeRename = func() {
		if !fired { // Claude saves a setting between our read and our rename
			fired = true
			b, _ := os.ReadFile(p)
			_ = os.WriteFile(p, []byte(strings.Replace(string(b), `"model": "x",`, `"model": "x", "theme": "dark",`, 1)), 0o644)
		}
	}
	if err := Install(p, "/opt/bin/switcch"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `"theme": "dark"`) || len(groupsFor(t, p, "Stop")) != 1 {
		t.Fatalf("a concurrent settings change must survive and hooks must be installed:\n%s", b)
	}
}

// TestMain keeps every test's edit lock out of the user's real data dir.
func TestMain(m *testing.M) {
	d, err := os.MkdirTemp("", "switcch-hooks-test")
	if err != nil {
		panic(err)
	}
	lockPath = func() string { return filepath.Join(d, "settings.lock") }
	code := m.Run()
	os.RemoveAll(d)
	os.Exit(code)
}
