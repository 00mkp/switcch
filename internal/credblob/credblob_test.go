package credblob

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func mustBlob(t *testing.T, exp time.Time) *Blob {
	t.Helper()
	raw := []byte(fmt.Sprintf(
		`{"claudeAiOauth":{"accessToken":"AT","refreshToken":"RT","expiresAt":%d,"scopes":["a"],"customField":"keep"},"mcpOAuth":{"x":1}}`,
		exp.UnixMilli()))
	b, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseMarshalPreservesUnknownKeys(t *testing.T) {
	b := mustBlob(t, time.Now().Add(time.Hour))
	if b.AccessToken() != "AT" {
		t.Fatalf("AccessToken = %q, want AT", b.AccessToken())
	}
	if b.RefreshToken() != "RT" {
		t.Fatalf("RefreshToken = %q, want RT", b.RefreshToken())
	}
	out, err := b.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(out, &top); err != nil {
		t.Fatal(err)
	}
	if _, ok := top["mcpOAuth"]; !ok {
		t.Fatal("Marshal dropped unknown top-level key mcpOAuth")
	}
	var oauth map[string]json.RawMessage
	if err := json.Unmarshal(top["claudeAiOauth"], &oauth); err != nil {
		t.Fatal(err)
	}
	if _, ok := oauth["customField"]; !ok {
		t.Fatal("Marshal dropped unknown nested key customField")
	}
}

func TestIsExpired(t *testing.T) {
	now := time.Now()
	if !mustBlob(t, now.Add(2*time.Minute)).IsExpired(now) {
		t.Fatal("token 2 min from expiry should be expired within the 5 min buffer")
	}
	if mustBlob(t, now.Add(time.Hour)).IsExpired(now) {
		t.Fatal("token 1 h from expiry should not be expired")
	}
}

func TestApplyRefresh(t *testing.T) {
	b := mustBlob(t, time.Now())
	ref := time.Now()
	b.ApplyRefresh("NEWAT", "NEWRT", 3600, []string{"s"})
	if b.AccessToken() != "NEWAT" {
		t.Fatalf("AccessToken = %q, want NEWAT", b.AccessToken())
	}
	if b.RefreshToken() != "NEWRT" {
		t.Fatalf("RefreshToken = %q, want NEWRT", b.RefreshToken())
	}
	if b.IsExpired(ref.Add(30 * time.Minute)) {
		t.Fatal("refreshed token (expires_in 3600s) should still be valid 30 min out")
	}
	out, err := b.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("customField")) {
		t.Fatal("ApplyRefresh dropped unknown nested key customField")
	}
}
