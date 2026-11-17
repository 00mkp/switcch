package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"github.com/00mkp/switcch/internal/account"
	"github.com/00mkp/switcch/internal/credblob"
	"github.com/00mkp/switcch/internal/credfile"
	"github.com/00mkp/switcch/internal/credsync"
	"github.com/00mkp/switcch/internal/oauth"
	"github.com/00mkp/switcch/internal/profile"
)

// Test seams / tunables.
var (
	serveTTL     = 180 * time.Second
	nowFn        = time.Now
	fetch        = Fetch
	tokenFor     = defaultTokenFor
	forceRefresh = defaultForceRefresh
	oauthRefresh = oauth.Refresh
	// jitterFn spreads refetch times (0..20% of TTL) so accounts don't all
	// refetch in lockstep. Zeroed in tests for determinism.
	jitterFn = func() time.Duration { return time.Duration(rand.Int63n(int64(serveTTL / 5))) }
)

type entry struct {
	Usage        Usage     `json:"usage"`
	ReadAt       time.Time `json:"readAt"`
	BlockedUntil time.Time `json:"blockedUntil,omitempty"`
}

func dataHome() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "switcch")
}

func cachePath() string { return filepath.Join(dataHome(), "usage-cache.json") }

// Cached returns the account's usage: a cached reading younger than serveTTL
// (plus jitter) is served; otherwise it fetches (coalesced under a file lock).
// A 401 triggers one forced credential refresh + retry. On a 429/error it
// records a backoff and serves the last good reading if there is one.
func Cached(ctx context.Context, a account.Account) (Usage, time.Time, error) {
	unlock, err := lock()
	if err != nil {
		return Usage{}, time.Time{}, err
	}
	defer unlock()

	cache := loadCache()
	e, ok := cache[a.Slot]
	now := nowFn()
	if ok && !e.ReadAt.IsZero() && now.Sub(e.ReadAt) < serveTTL+jitterFn() {
		return e.Usage, e.ReadAt, nil
	}
	if ok && now.Before(e.BlockedUntil) {
		if !e.ReadAt.IsZero() {
			return e.Usage, e.ReadAt, nil
		}
		return Usage{}, time.Time{}, fmt.Errorf("usage: backing off for slot %d until %s", a.Slot, e.BlockedUntil.Format(time.RFC3339))
	}

	tok, err := tokenFor(a)
	if err != nil {
		return Usage{}, time.Time{}, err
	}
	u, err := fetch(ctx, tok)
	if errors.Is(err, ErrUnauthorized) {
		if tok, err = forceRefresh(a); err != nil {
			return Usage{}, time.Time{}, err
		}
		u, err = fetch(ctx, tok)
	}
	if err != nil {
		backoff := 60 * time.Second
		var rl *RateLimitedError
		if errors.As(err, &rl) {
			backoff = rl.RetryAfter
		}
		e.BlockedUntil = now.Add(backoff)
		cache[a.Slot] = e
		saveCache(cache)
		if !e.ReadAt.IsZero() {
			return e.Usage, e.ReadAt, nil // serve the last good reading
		}
		return Usage{}, time.Time{}, err
	}

	e = entry{Usage: u, ReadAt: now} // success clears any backoff
	cache[a.Slot] = e
	saveCache(cache)
	return u, now, nil
}

func loadCache() map[int]entry {
	m := map[int]entry{}
	b, err := os.ReadFile(cachePath())
	if err != nil {
		return m
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return map[int]entry{} // tolerate a corrupt cache
	}
	return m
}

func saveCache(m map[int]entry) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(dataHome(), 0o700)
	tmp := cachePath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, cachePath())
	}
}

func lock() (func(), error) {
	lp := filepath.Join(dataHome(), ".usage.lock")
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
	return nil, errors.New("usage: cache lock timeout")
}

// defaultTokenFor returns a valid access token, refreshing if the stored token
// is expired.
func defaultTokenFor(a account.Account) (string, error) {
	cred, err := credsync.Current(a) // prefer Claude's live profile-keychain token
	if err != nil {
		return "", err
	}
	blob, err := credblob.Parse([]byte(cred))
	if err != nil {
		return "", err
	}
	if !blob.IsExpired(time.Now()) {
		return blob.AccessToken(), nil
	}
	return refreshToken(a, false)
}

// defaultForceRefresh refreshes regardless of expiry (used after a 401).
func defaultForceRefresh(a account.Account) (string, error) { return refreshToken(a, true) }

// refreshToken refreshes the stored credential under the SAME profile-dir lock a
// live session/launch uses, re-reading the credential inside the lock so it
// never refreshes from a stale (already-rotated) token — the double-spend that
// would otherwise kill a healthy account.
func refreshToken(a account.Account, force bool) (string, error) {
	dir := profile.Dir(a)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	var token string
	err := oauth.WithRefreshLock(dir, func() error {
		cred, err := credsync.Current(a) // freshest: Claude's profile keychain, else backup
		if err != nil {
			return err
		}
		blob, err := credblob.Parse([]byte(cred))
		if err != nil {
			return err
		}
		if !force && !blob.IsExpired(time.Now()) {
			token = blob.AccessToken() // another holder already refreshed
			return nil
		}
		tr, err := oauthRefresh(context.Background(), blob.RefreshToken())
		if err != nil {
			return err
		}
		blob.ApplyRefresh(tr.AccessToken, tr.RefreshToken, tr.ExpiresIn, tr.Scopes)
		nb, err := blob.Marshal()
		if err != nil {
			return err
		}
		token = tr.AccessToken
		return credfile.Write(a.Slot, string(nb))
	})
	return token, err
}
