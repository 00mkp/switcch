// Package credblob parses and patches the Claude Code OAuth credential JSON.
//
// The on-disk/keychain shape is:
//
//	{"claudeAiOauth":{"accessToken","refreshToken","expiresAt"(ms epoch),"scopes":[...],...},
//	 "mcpOAuth":{...}, ...}
//
// Unknown keys at both the top level and inside claudeAiOauth must survive a
// parse→patch→marshal round-trip, so the structure is kept as raw maps.
package credblob

import (
	"encoding/json"
	"fmt"
	"time"
)

// expiryBuffer treats a token within this window of expiry as already expired.
const expiryBuffer = 5 * time.Minute

const oauthKey = "claudeAiOauth"

// Blob is a parsed credential JSON that preserves every key it did not model.
type Blob struct {
	top   map[string]json.RawMessage
	oauth map[string]json.RawMessage
}

// Parse decodes a credential blob, keeping all unknown keys.
func Parse(data []byte) (*Blob, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("credblob: parse: %w", err)
	}
	oauth := map[string]json.RawMessage{}
	if raw, ok := top[oauthKey]; ok {
		if err := json.Unmarshal(raw, &oauth); err != nil {
			return nil, fmt.Errorf("credblob: parse %s: %w", oauthKey, err)
		}
	}
	return &Blob{top: top, oauth: oauth}, nil
}

func (b *Blob) str(key string) string {
	raw, ok := b.oauth[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// AccessToken returns the OAuth bearer token.
func (b *Blob) AccessToken() string { return b.str("accessToken") }

// RefreshToken returns the rotating refresh token.
func (b *Blob) RefreshToken() string { return b.str("refreshToken") }

// IsExpired reports whether the token is expired as of now, treating a token
// within expiryBuffer of its expiresAt (or with no expiresAt) as expired.
func (b *Blob) IsExpired(now time.Time) bool {
	raw, ok := b.oauth["expiresAt"]
	if !ok {
		return true
	}
	var expMs int64
	if err := json.Unmarshal(raw, &expMs); err != nil {
		return true
	}
	return now.Add(expiryBuffer).UnixMilli() >= expMs
}

// ApplyRefresh writes the result of a token refresh back into the blob,
// preserving every other key. refreshToken and scopes are updated only when
// non-empty/non-nil (the token endpoint may omit a rotated refresh token).
func (b *Blob) ApplyRefresh(accessToken, refreshToken string, expiresInSec int, scopes []string) {
	b.setStr("accessToken", accessToken)
	if refreshToken != "" {
		b.setStr("refreshToken", refreshToken)
	}
	expMs := time.Now().Add(time.Duration(expiresInSec) * time.Second).UnixMilli()
	b.oauth["expiresAt"], _ = json.Marshal(expMs)
	if scopes != nil {
		b.oauth["scopes"], _ = json.Marshal(scopes)
	}
}

func (b *Blob) setStr(key, val string) {
	raw, _ := json.Marshal(val)
	b.oauth[key] = raw
}

// Marshal re-serializes the blob, folding the (possibly patched) oauth map back
// under claudeAiOauth and keeping all other top-level keys.
func (b *Blob) Marshal() ([]byte, error) {
	oauthRaw, err := json.Marshal(b.oauth)
	if err != nil {
		return nil, fmt.Errorf("credblob: marshal %s: %w", oauthKey, err)
	}
	b.top[oauthKey] = oauthRaw
	out, err := json.Marshal(b.top)
	if err != nil {
		return nil, fmt.Errorf("credblob: marshal: %w", err)
	}
	return out, nil
}
