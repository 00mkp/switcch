// Package profile builds and seeds the isolated per-account Claude Code config
// directories switcch launches sessions against.
package profile

import (
	"os"
	"path/filepath"
	"regexp"

	"github.com/00mkp/switcch/internal/account"
	"golang.org/x/text/unicode/norm"
)

var slugUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// slug makes an email safe for a path segment: NFC-normalize, then replace any
// character outside [A-Za-z0-9._-] with '_'.
func slug(email string) string {
	return slugUnsafe.ReplaceAllString(norm.NFC.String(email), "_")
}

func dataHome() string {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return x
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}

// Dir is the profile directory (the CLAUDE_CONFIG_DIR) for an account.
func Dir(a account.Account) string {
	return filepath.Join(dataHome(), "switcch", "profiles", slug(a.Email))
}
