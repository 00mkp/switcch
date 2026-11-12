// Package keychain derives the Claude Code credential Keychain identifiers and
// reads/writes those items on macOS.
//
// The service-name recipe mirrors Claude Code's own derivation (verified in the
// 2.1.318 bundle): with CLAUDE_CONFIG_DIR set, the service is
// "Claude Code-credentials-" + first 8 hex of sha256(NFC(dir)); unset, it is the
// bare "Claude Code-credentials". Hash the exact exported string — no realpath,
// no trailing-slash stripping, no ~ expansion.
package keychain

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/user"
	"regexp"

	"golang.org/x/text/unicode/norm"
)

const credentialsBase = "Claude Code-credentials"

var accountNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ServiceName returns the macOS Keychain service name Claude Code uses for the
// OAuth credential of a profile rooted at configDir. An empty configDir yields
// the default (unsuffixed) service.
func ServiceName(configDir string) string {
	if configDir == "" {
		return credentialsBase
	}
	sum := sha256.Sum256([]byte(norm.NFC.String(configDir)))
	return credentialsBase + "-" + hex.EncodeToString(sum[:])[:8]
}

// AccountName returns the Keychain account used for Claude Code credential items:
// $USER, else the OS username, else the constant "claude-code-user" when the
// chosen value contains characters outside [A-Za-z0-9._-].
func AccountName() string {
	cand := os.Getenv("USER")
	if cand == "" {
		if u, err := user.Current(); err == nil {
			cand = u.Username
		}
	}
	if !accountNameRe.MatchString(cand) {
		return "claude-code-user"
	}
	return cand
}
