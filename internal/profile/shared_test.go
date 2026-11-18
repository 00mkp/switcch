package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSyncShared(t *testing.T) {
	home := t.TempDir()
	prof := t.TempDir()
	mustWrite(t, filepath.Join(home, "settings.json"), "{}")
	if err := os.Mkdir(filepath.Join(home, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	realMD := filepath.Join(home, "real-CLAUDE.md")
	mustWrite(t, realMD, "# hi")
	if err := os.Symlink(realMD, filepath.Join(home, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	// Note: no "rules" in home — must be skipped, not an error.

	if err := SyncShared(prof, home); err != nil {
		t.Fatal(err)
	}

	if got, _ := os.Readlink(filepath.Join(prof, "settings.json")); got != filepath.Join(home, "settings.json") {
		t.Fatalf("settings.json link = %q", got)
	}
	if got, _ := os.Readlink(filepath.Join(prof, "skills")); got != filepath.Join(home, "skills") {
		t.Fatalf("skills link = %q", got)
	}
	// A symlinked source must be resolved to its target, not linked link-to-link.
	if got, _ := os.Readlink(filepath.Join(prof, "CLAUDE.md")); got != realMD {
		t.Fatalf("CLAUDE.md link = %q, want resolved %q", got, realMD)
	}
	if _, err := os.Lstat(filepath.Join(prof, "rules")); !os.IsNotExist(err) {
		t.Fatalf("missing source 'rules' must be skipped; lstat err = %v", err)
	}

	mb, err := os.ReadFile(filepath.Join(prof, ".switcch-shared.json"))
	if err != nil {
		t.Fatal(err)
	}
	var man struct {
		Linked []string `json:"linked"`
	}
	if err := json.Unmarshal(mb, &man); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"settings.json", "skills", "CLAUDE.md"} {
		if !slices.Contains(man.Linked, want) {
			t.Fatalf("manifest missing %q: %v", want, man.Linked)
		}
	}

	if err := SyncShared(prof, home); err != nil {
		t.Fatalf("second SyncShared should be a no-op, got %v", err)
	}
}

func TestScrubEnv(t *testing.T) {
	in := []string{
		"PATH=/bin",
		"ANTHROPIC_API_KEY=secret",
		"ANTHROPIC_AUTH_TOKEN=secret",
		"CLAUDE_CODE_OAUTH_TOKEN=secret",
		"CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR=3",
		"CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR=4",
		"HOME=/home/x",
	}
	out := ScrubEnv(in)
	for _, e := range out {
		if strings.HasPrefix(e, "ANTHROPIC_") || strings.HasPrefix(e, "CLAUDE_CODE_OAUTH_TOKEN") || strings.HasPrefix(e, "CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR") {
			t.Fatalf("scrubbed var leaked: %q", e)
		}
	}
	if !slices.Contains(out, "PATH=/bin") || !slices.Contains(out, "HOME=/home/x") {
		t.Fatalf("dropped a safe var: %v", out)
	}
	if len(out) != 2 {
		t.Fatalf("want exactly 2 vars kept, got %d: %v", len(out), out)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Converting a profile's real projects/ and history.jsonl into shared links must
// merge their contents into ~/.claude first — never lose a chat.
func TestSyncSharedMergesExistingTranscripts(t *testing.T) {
	home := t.TempDir()
	prof := t.TempDir()
	// home side
	if err := os.MkdirAll(filepath.Join(home, "projects", "-y"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(home, "projects", "-y", "def.jsonl"), "home-chat")
	mustWrite(t, filepath.Join(home, "history.jsonl"), "h\n")
	// profile side (pre-existing, real)
	if err := os.MkdirAll(filepath.Join(prof, "projects", "-x"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(prof, "projects", "-x", "abc.jsonl"), "profile-chat")
	mustWrite(t, filepath.Join(prof, "history.jsonl"), "p\n")

	if err := SyncShared(prof, home); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"projects", "history.jsonl"} {
		got, err := os.Readlink(filepath.Join(prof, name))
		if err != nil || got != filepath.Join(home, name) {
			t.Fatalf("%s should be a link to home, got %q err %v", name, got, err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(home, "projects", "-x", "abc.jsonl")); err != nil || string(b) != "profile-chat" {
		t.Fatalf("profile chat was not merged into home: %q %v", b, err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "projects", "-y", "def.jsonl")); string(b) != "home-chat" {
		t.Fatal("existing home chat was disturbed")
	}
	hist, _ := os.ReadFile(filepath.Join(home, "history.jsonl"))
	if !strings.Contains(string(hist), "h\n") || !strings.Contains(string(hist), "p\n") {
		t.Fatalf("history not merged: %q", hist)
	}
}

func TestSyncSharedCreatesMissingTranscriptSources(t *testing.T) {
	home, prof := t.TempDir(), t.TempDir()
	if err := SyncShared(prof, home); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"projects", "history.jsonl"} {
		if fi, err := os.Lstat(filepath.Join(prof, n)); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s must be shared even when ~/.claude has none yet: %v", n, err)
		}
	}
}

func TestSyncSharedRefusesSelfTargetAndNeverDeletes(t *testing.T) {
	home, prof := t.TempDir(), t.TempDir()
	chat := filepath.Join(prof, "projects", "-x", "abc.jsonl")
	_ = os.MkdirAll(filepath.Dir(chat), 0o700)
	_ = os.WriteFile(chat, []byte("chat"), 0o600)
	// ~/.claude/projects already points INTO the profile's real dir.
	if err := os.Symlink(filepath.Join(prof, "projects"), filepath.Join(home, "projects")); err != nil {
		t.Fatal(err)
	}
	if err := SyncShared(prof, home); err == nil {
		t.Fatal("a shared target that resolves to the profile's own dir must be refused")
	}
	if b, err := os.ReadFile(chat); err != nil || string(b) != "chat" {
		t.Fatalf("chat must survive: %q %v", b, err)
	}
}

func TestSyncSharedKeepsOlderClashAside(t *testing.T) {
	home, prof := t.TempDir(), t.TempDir()
	for _, d := range []string{home, prof} {
		_ = os.MkdirAll(filepath.Join(d, "projects", "-x"), 0o700)
	}
	older := filepath.Join(prof, "projects", "-x", "a.jsonl")
	_ = os.WriteFile(older, []byte("profile-old"), 0o600)
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(older, old, old)
	_ = os.WriteFile(filepath.Join(home, "projects", "-x", "a.jsonl"), []byte("home-new"), 0o600)
	if err := SyncShared(prof, home); err != nil {
		t.Fatal(err)
	}
	found := false
	_ = filepath.Walk(prof, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			if b, _ := os.ReadFile(p); string(b) == "profile-old" {
				found = true
			}
		}
		return nil
	})
	if !found {
		t.Fatal("the older clashing copy must be kept aside, not deleted")
	}
}

func TestMergeHistoryAddsMissingNewline(t *testing.T) {
	home, prof := t.TempDir(), t.TempDir()
	_ = os.WriteFile(filepath.Join(home, "history.jsonl"), []byte(`{"h":1}`), 0o600)
	_ = os.WriteFile(filepath.Join(prof, "history.jsonl"), []byte("{\"p\":1}\n"), 0o600)
	if err := SyncShared(prof, home); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(home, "history.jsonl"))
	if string(b) != "{\"h\":1}\n{\"p\":1}\n" {
		t.Fatalf("history = %q", b)
	}
}
