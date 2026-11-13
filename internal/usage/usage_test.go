package usage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

const fixture = `{
  "five_hour": {"utilization": 2.0, "resets_at": "2026-09-23T12:59:59.593033+00:00"},
  "seven_day": {"utilization": 59.0, "resets_at": "2026-09-30T00:00:00+00:00"},
  "limits": [
    {"kind": "session", "percent": 2},
    {"kind": "weekly_all", "percent": 59},
    {"kind": "weekly_scoped", "percent": 100, "is_active": true, "severity": "critical",
     "scope": {"model": {"display_name": "Fable"}}}
  ],
  "nimbus_quill": "ignored"
}`

func TestFetchParsesWindowsAndModelCaps(t *testing.T) {
	var gotAuth, gotBeta string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBeta = r.Header.Get("anthropic-beta")
		_, _ = w.Write([]byte(fixture))
	}))
	defer srv.Close()
	endpoint = srv.URL

	u, err := Fetch(context.Background(), "AT")
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer AT" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotBeta != "oauth-2025-04-20" {
		t.Fatalf("anthropic-beta = %q", gotBeta)
	}
	if u.FiveHour.Utilization != 2 {
		t.Fatalf("five_hour utilization = %v", u.FiveHour.Utilization)
	}
	if !u.FiveHour.HasReset {
		t.Fatal("five_hour should carry a reset time")
	}
	if u.SevenDay.Utilization != 59 {
		t.Fatalf("seven_day utilization = %v", u.SevenDay.Utilization)
	}
	if len(u.Models) != 1 {
		t.Fatalf("models = %+v, want 1 (Fable)", u.Models)
	}
	m := u.Models[0]
	if m.Model != "Fable" || m.Percent != 100 || !m.Active {
		t.Fatalf("model cap = %+v", m)
	}
}

func TestFetchUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	endpoint = srv.URL

	if _, err := Fetch(context.Background(), "AT"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}
