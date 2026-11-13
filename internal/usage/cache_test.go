package usage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/00mkp/switcch/internal/account"
)

func saveSeams(t *testing.T) {
	t.Helper()
	a, b, c, d, e, j := serveTTL, nowFn, fetch, tokenFor, forceRefresh, jitterFn
	t.Cleanup(func() { serveTTL, nowFn, fetch, tokenFor, forceRefresh, jitterFn = a, b, c, d, e, j })
	jitterFn = func() time.Duration { return 0 } // deterministic TTL in tests
}

func TestCacheBacksOffOn429AndServesStale(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	saveSeams(t)
	cur := time.Now()
	nowFn = func() time.Time { return cur }
	tokenFor = func(account.Account) (string, error) { return "AT", nil }
	forceRefresh = func(account.Account) (string, error) { return "AT", nil }
	calls := 0
	fetch = func(ctx context.Context, tok string) (Usage, error) {
		calls++
		if calls == 1 {
			return Usage{FiveHour: Window{Utilization: 5}}, nil
		}
		return Usage{}, &RateLimitedError{RetryAfter: 300 * time.Second}
	}
	a := account.Account{Slot: 1}

	if _, _, err := Cached(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	cur = cur.Add(serveTTL + time.Second) // force a refetch → hits the 429
	u, _, err := Cached(context.Background(), a)
	if err != nil {
		t.Fatalf("on 429 with a prior reading, Cached should serve stale, got %v", err)
	}
	if u.FiveHour.Utilization != 5 {
		t.Fatalf("served usage = %v, want the stale 5", u.FiveHour.Utilization)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	cur = cur.Add(10 * time.Second) // still inside the 300s backoff window
	if _, _, err := Cached(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("within backoff window Cached must not refetch; calls = %d", calls)
	}
}

func TestCacheServesWithinTTLThenRefetches(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	saveSeams(t)
	base := time.Now()
	cur := base
	nowFn = func() time.Time { return cur }
	tokenFor = func(account.Account) (string, error) { return "AT", nil }
	forceRefresh = func(account.Account) (string, error) { return "AT2", nil }
	calls := 0
	fetch = func(ctx context.Context, tok string) (Usage, error) {
		calls++
		return Usage{FiveHour: Window{Utilization: float64(calls)}}, nil
	}
	a := account.Account{Slot: 1}

	if _, _, err := Cached(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("first call should fetch; calls=%d", calls)
	}
	if _, _, err := Cached(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("within TTL should serve cache; calls=%d", calls)
	}
	cur = base.Add(serveTTL + time.Second)
	if _, _, err := Cached(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("after TTL should refetch; calls=%d", calls)
	}
}

func TestCacheRefreshesOn401(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	saveSeams(t)
	nowFn = time.Now
	tokenFor = func(account.Account) (string, error) { return "stale", nil }
	refreshCalls := 0
	forceRefresh = func(account.Account) (string, error) { refreshCalls++; return "fresh", nil }
	fcalls := 0
	fetch = func(ctx context.Context, tok string) (Usage, error) {
		fcalls++
		if tok == "stale" {
			return Usage{}, ErrUnauthorized
		}
		return Usage{}, nil
	}
	if _, _, err := Cached(context.Background(), account.Account{Slot: 1}); err != nil {
		t.Fatal(err)
	}
	if refreshCalls != 1 {
		t.Fatalf("expected 1 force-refresh, got %d", refreshCalls)
	}
	if fcalls != 2 {
		t.Fatalf("expected fetch retried once after 401, got %d", fcalls)
	}
}

func TestCacheCorruptFileRefetches(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	saveSeams(t)
	nowFn = time.Now
	tokenFor = func(account.Account) (string, error) { return "AT", nil }
	forceRefresh = func(account.Account) (string, error) { return "AT", nil }
	calls := 0
	fetch = func(ctx context.Context, tok string) (Usage, error) { calls++; return Usage{}, nil }

	_ = os.MkdirAll(filepath.Dir(cachePath()), 0o700)
	_ = os.WriteFile(cachePath(), []byte("{not json"), 0o600)

	if _, _, err := Cached(context.Background(), account.Account{Slot: 1}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("corrupt cache should refetch; calls=%d", calls)
	}
}
