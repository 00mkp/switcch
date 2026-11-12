package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// SharedItems are the ~/.claude entries symlinked into every profile so all
// sessions share one config. plugins is included (spike finding: unshared
// plugins caused a "reload plugins" prompt on cross-account resume).
var SharedItems = []string{
	"settings.json", "keybindings.json", "CLAUDE.md",
	"skills", "commands", "agents", "rules", "plugins",
}

// scrubKeys are auth-override env vars removed before launching a session; they
// would bypass the profile's OAuth account selection.
var scrubKeys = map[string]bool{
	"ANTHROPIC_API_KEY":                       true,
	"ANTHROPIC_AUTH_TOKEN":                    true,
	"CLAUDE_CODE_OAUTH_TOKEN":                 true,
	"CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR": true,
	"CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR":     true,
}

// SyncShared symlinks each existing SharedItems entry from claudeHome into
// profileDir, resolving a symlinked source to its target (so Claude's atomic
// settings write never clobbers a link-to-link). Missing sources are skipped.
// It is idempotent and records what it manages in .switcch-shared.json.
func SyncShared(profileDir, claudeHome string) error {
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return err
	}
	linked := []string{}
	for _, name := range SharedItems {
		src := filepath.Join(claudeHome, name)
		fi, err := os.Lstat(src)
		if err != nil {
			continue // missing source — skip cleanly
		}
		target := src
		if fi.Mode()&os.ModeSymlink != 0 {
			if resolved, err := os.Readlink(src); err == nil {
				if !filepath.IsAbs(resolved) {
					resolved = filepath.Join(filepath.Dir(src), resolved)
				}
				target = resolved
			}
		}
		dst := filepath.Join(profileDir, name)
		if cur, err := os.Readlink(dst); err == nil && cur == target {
			linked = append(linked, name)
			continue
		}
		_ = os.Remove(dst)
		if err := os.Symlink(target, dst); err != nil {
			return err
		}
		linked = append(linked, name)
	}
	man, err := json.MarshalIndent(struct {
		Linked []string `json:"linked"`
	}{linked}, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(profileDir, ".switcch-shared.json"), man, 0o600)
}

// ScrubEnv returns env with the auth-override variables removed.
func ScrubEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if scrubKeys[k] {
			continue
		}
		out = append(out, e)
	}
	return out
}
