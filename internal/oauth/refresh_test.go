package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRefreshSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "AT", "refresh_token": "RT2", "expires_in": 3600, "scope": "a b",
		})
	}))
	defer srv.Close()
	tokenEndpoint = srv.URL

	got, err := Refresh(context.Background(), "RT")
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "AT" || got.RefreshToken != "RT2" || got.ExpiresIn != 3600 {
		t.Fatalf("got %+v", got)
	}
	if len(got.Scopes) != 2 || got.Scopes[0] != "a" || got.Scopes[1] != "b" {
		t.Fatalf("scopes = %v", got.Scopes)
	}
}

func TestRefreshInvalidGrant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
	}))
	defer srv.Close()
	tokenEndpoint = srv.URL

	if _, err := Refresh(context.Background(), "RT"); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("err = %v, want ErrInvalidGrant", err)
	}
}

func TestRefreshServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	tokenEndpoint = srv.URL

	_, err := Refresh(context.Background(), "RT")
	if err == nil || errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("err = %v, want a generic (non-invalid_grant) error", err)
	}
}

func TestWithRefreshLock(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, ".oauth_refresh.lock")
	ran := false
	err := WithRefreshLock(dir, func() error {
		ran = true
		if _, e := os.Stat(lock); e != nil {
			t.Fatalf("primary lock not held during fn: %v", e)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("fn was not run")
	}
	if _, e := os.Stat(lock); !os.IsNotExist(e) {
		t.Fatalf("primary lock not released: %v", e)
	}
}
