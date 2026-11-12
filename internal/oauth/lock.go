package oauth

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"time"
)

const (
	lockStale   = 60 * time.Second
	lockRetries = 6
)

// acquire takes a directory-based lock (matching Claude Code's proper-lockfile
// scheme): mkdir succeeds for exactly one holder; a lock older than lockStale is
// treated as abandoned and broken.
func acquire(path string) error {
	for i := 0; i < lockRetries; i++ {
		if err := os.Mkdir(path, 0o700); err == nil {
			return nil
		} else if !os.IsExist(err) {
			return err
		}
		if fi, e := os.Stat(path); e == nil && time.Since(fi.ModTime()) > lockStale {
			_ = os.Remove(path)
			continue
		}
		time.Sleep(time.Duration(1000+rand.Intn(1000)) * time.Millisecond)
	}
	return fmt.Errorf("oauth: could not acquire lock %s", path)
}

// WithRefreshLock runs fn while holding both refresh locks Claude Code uses for
// a config dir: <dir>/.oauth_refresh.lock and the legacy <dir>.lock. This keeps
// switcch's out-of-band refresh from colliding with a live Claude's rotation.
func WithRefreshLock(configDir string, fn func() error) error {
	primary := filepath.Join(configDir, ".oauth_refresh.lock")
	legacy := configDir + ".lock"
	if err := acquire(primary); err != nil {
		return err
	}
	defer os.Remove(primary)
	if err := acquire(legacy); err != nil {
		return err
	}
	defer os.Remove(legacy)
	return fn()
}
