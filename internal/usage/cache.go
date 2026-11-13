package usage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/00mkp/switcch/internal/account"
	"github.com/00mkp/switcch/internal/credblob"
	"github.com/00mkp/switcch/internal/credfile"
	"github.com/00mkp/switcch/internal/oauth"
)

// Test seams / tunables.
var (
	serveTTL     = 180 * time.Second
	nowFn        = time.Now
	fetch        = Fetch
	tokenFor     = defaultTokenFor
	forceRefresh = defaultForceRefresh
)

type entry struct {
	Usage  Usage     `json:"usage"`
	ReadAt time.Time `json:"readAt"`
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

// Cached returns the account's usage, serving a cached reading younger than
// serveTTL and otherwise fetching (coalesced under a file lock). A 401 triggers
// one forced credential refresh + retry. Returns the usage and the time read.
func Cached(ctx context.Context, a account.Account) (Usage, time.Time, error) {
	unlock, err := lock()
	if err != nil {
		return Usage{}, time.Time{}, err
	}
	defer unlock()

	cache := loadCache()
	if e, ok := cache[a.Slot]; ok && nowFn().Sub(e.ReadAt) < serveTTL {
		return e.Usage, e.ReadAt, nil
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
		return Usage{}, time.Time{}, err
	}

	now := nowFn()
	cache[a.Slot] = entry{Usage: u, ReadAt: now}
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

// lock takes a short-lived directory lock around the cache read-modify-write so
// concurrent `switcch list` calls coalesce instead of each hitting the endpoint.
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

// defaultTokenFor returns a valid access token for the account, refreshing the
// stored credential if it is expired.
func defaultTokenFor(a account.Account) (string, error) {
	cred, err := credfile.Read(a.Slot)
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
	return refreshAndStore(a, blob)
}

// defaultForceRefresh refreshes regardless of expiry (used after a 401).
func defaultForceRefresh(a account.Account) (string, error) {
	cred, err := credfile.Read(a.Slot)
	if err != nil {
		return "", err
	}
	blob, err := credblob.Parse([]byte(cred))
	if err != nil {
		return "", err
	}
	return refreshAndStore(a, blob)
}

func refreshAndStore(a account.Account, blob *credblob.Blob) (string, error) {
	var token string
	err := oauth.WithRefreshLock(credfile.Path(a.Slot), func() error {
		tr, err := oauth.Refresh(context.Background(), blob.RefreshToken())
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
