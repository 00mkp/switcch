// Package oauth refreshes Claude OAuth credentials out-of-band and coordinates
// with Claude Code's own refresh locks so a rotating refresh token has a single
// owner at a time.
package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// clientID is Claude Code's public OAuth client id.
const clientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

// tokenEndpoint is overridable in tests.
var tokenEndpoint = "https://platform.claude.com/v1/oauth/token"

// ErrInvalidGrant means the refresh token was rejected (spent/rotated/revoked);
// the account must be re-added.
var ErrInvalidGrant = errors.New("oauth: refresh token rejected (invalid_grant)")

// TokenResponse is the result of a successful refresh.
type TokenResponse struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	Scopes       []string
}

// Refresh exchanges a refresh token for a new access token.
func Refresh(ctx context.Context, refreshToken string) (TokenResponse, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return TokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return TokenResponse{}, fmt.Errorf("oauth: refresh request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		if e.Error == "invalid_grant" {
			return TokenResponse{}, ErrInvalidGrant
		}
		return TokenResponse{}, fmt.Errorf("oauth: refresh failed (status %d): %s", resp.StatusCode, e.Error)
	}

	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return TokenResponse{}, fmt.Errorf("oauth: parse token response: %w", err)
	}
	var scopes []string
	if out.Scope != "" {
		scopes = strings.Fields(out.Scope)
	}
	return TokenResponse{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		ExpiresIn:    out.ExpiresIn,
		Scopes:       scopes,
	}, nil
}
