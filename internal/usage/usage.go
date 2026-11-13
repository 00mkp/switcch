// Package usage queries Claude's OAuth usage endpoint for an account's rate-limit
// windows (5h/7d) and model-scoped weekly caps.
package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// endpoint is overridable in tests.
var endpoint = "https://api.anthropic.com/api/oauth/usage"

// ErrUnauthorized means the access token is expired/invalid (HTTP 401).
var ErrUnauthorized = errors.New("usage: unauthorized (token expired)")

// Window is one rate-limit window's utilization and reset time.
type Window struct {
	Utilization float64
	ResetsAt    time.Time
	HasReset    bool
}

// ModelCap is a model-scoped weekly cap (from limits[]).
type ModelCap struct {
	Model   string
	Percent float64
	Active  bool
}

// Usage is an account's usage snapshot.
type Usage struct {
	FiveHour Window
	SevenDay Window
	Models   []ModelCap
}

type winJSON struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    *string `json:"resets_at"`
}

func (w winJSON) toWindow() Window {
	out := Window{Utilization: w.Utilization}
	if w.ResetsAt != nil {
		if t, err := time.Parse(time.RFC3339, *w.ResetsAt); err == nil {
			out.ResetsAt = t
			out.HasReset = true
		}
	}
	return out
}

// Fetch retrieves the usage snapshot for an account access token.
func Fetch(ctx context.Context, accessToken string) (Usage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Usage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Usage{}, fmt.Errorf("usage: request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return Usage{}, ErrUnauthorized
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return Usage{}, fmt.Errorf("usage: status %d: %s", resp.StatusCode, string(body))
	}

	var raw struct {
		FiveHour winJSON `json:"five_hour"`
		SevenDay winJSON `json:"seven_day"`
		Limits   []struct {
			Kind     string  `json:"kind"`
			Percent  float64 `json:"percent"`
			IsActive bool    `json:"is_active"`
			Scope    *struct {
				Model *struct {
					DisplayName string `json:"display_name"`
				} `json:"model"`
			} `json:"scope"`
		} `json:"limits"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Usage{}, fmt.Errorf("usage: parse: %w", err)
	}

	u := Usage{FiveHour: raw.FiveHour.toWindow(), SevenDay: raw.SevenDay.toWindow()}
	for _, l := range raw.Limits {
		if l.Kind == "weekly_scoped" && l.Scope != nil && l.Scope.Model != nil {
			u.Models = append(u.Models, ModelCap{
				Model:   l.Scope.Model.DisplayName,
				Percent: l.Percent,
				Active:  l.IsActive,
			})
		}
	}
	return u, nil
}
