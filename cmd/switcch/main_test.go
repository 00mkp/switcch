//go:build darwin

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/00mkp/switcch/internal/account"
	"github.com/00mkp/switcch/internal/oauth"
	"github.com/00mkp/switcch/internal/session"
	"github.com/00mkp/switcch/internal/usage"
)

func TestStatusJSON(t *testing.T) {
	accts := []account.Account{{Slot: 1, Email: "a@x.com", Alias: "personal"}}
	sessions := []session.Session{{Slot: 1, PID: 99, Cwd: "/p"}}
	usageOf := func(account.Account) (usage.Usage, bool) {
		return usage.Usage{
			FiveHour: usage.Window{Utilization: 7},
			SevenDay: usage.Window{Utilization: 11},
			Models:   []usage.ModelCap{{Model: "Fable", Percent: 100, Active: true}},
		}, true
	}
	b, err := statusJSON(accts, sessions, usageOf)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Accounts []struct {
			Slot     int    `json:"slot"`
			Email    string `json:"email"`
			Alias    string `json:"alias"`
			FiveHour struct {
				Utilization float64 `json:"utilization"`
			} `json:"fiveHour"`
			Models []struct {
				Model   string  `json:"model"`
				Percent float64 `json:"percent"`
			} `json:"models"`
			Sessions []struct {
				PID int    `json:"pid"`
				Cwd string `json:"cwd"`
			} `json:"sessions"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("status json did not parse: %v\n%s", err, b)
	}
	if len(parsed.Accounts) != 1 {
		t.Fatalf("accounts = %d", len(parsed.Accounts))
	}
	a := parsed.Accounts[0]
	if a.Slot != 1 || a.Email != "a@x.com" || a.FiveHour.Utilization != 7 {
		t.Fatalf("account = %+v", a)
	}
	if len(a.Models) != 1 || a.Models[0].Model != "Fable" {
		t.Fatalf("models = %+v", a.Models)
	}
	if len(a.Sessions) != 1 || a.Sessions[0].PID != 99 {
		t.Fatalf("sessions = %+v", a.Sessions)
	}
	if strings.Contains(string(b), "accessToken") || strings.Contains(string(b), "refreshToken") {
		t.Fatal("status --json leaked a secret field")
	}
}

func TestRenderList(t *testing.T) {
	accts := []account.Account{
		{Slot: 1, Email: "a@x.com", Alias: "personal"},
		{Slot: 2, Email: "b@y.com", Alias: "work"},
	}
	sessions := []session.Session{{Slot: 2, PID: 123, Cwd: "/w"}}
	usageOf := func(a account.Account) (usage.Usage, bool) {
		if a.Slot == 1 {
			return usage.Usage{FiveHour: usage.Window{Utilization: 12}, SevenDay: usage.Window{Utilization: 40}}, true
		}
		return usage.Usage{FiveHour: usage.Window{Utilization: 3}, SevenDay: usage.Window{Utilization: 5}}, true
	}
	out := renderList(accts, sessions, usageOf)

	if !strings.Contains(out, "personal") || !strings.Contains(out, "work") {
		t.Fatalf("missing account labels:\n%s", out)
	}
	if !strings.Contains(out, "12%") || !strings.Contains(out, "5h") {
		t.Fatalf("missing usage numbers:\n%s", out)
	}
	if !strings.Contains(out, "/w") || !strings.Contains(out, "123") {
		t.Fatalf("missing the work session:\n%s", out)
	}
	if n := strings.Count(out, "session"); n != 1 {
		t.Fatalf("want exactly one session line (under work), got %d:\n%s", n, out)
	}
}

func TestFriendlyLaunchErrorInvalidGrant(t *testing.T) {
	wrapped := fmt.Errorf("launch: refresh slot 2: %w", oauth.ErrInvalidGrant)
	msg := friendlyLaunchError("work", wrapped).Error()
	if !strings.Contains(msg, "switcch add") || !strings.Contains(msg, "work") {
		t.Fatalf("message = %q, want it to mention `switcch add` and the account", msg)
	}
}

func TestFriendlyLaunchErrorOther(t *testing.T) {
	other := errors.New("boom")
	if got := friendlyLaunchError("work", other); got != other {
		t.Fatalf("non-invalid_grant error should pass through unchanged, got %v", got)
	}
}

func TestRoute(t *testing.T) {
	cases := []struct {
		args      []string
		kind, ref string
		wantErr   bool
	}{
		{args: []string{"add"}, kind: "add"},
		{args: []string{"accounts"}, kind: "accounts"},
		{args: []string{"list"}, kind: "list"},
		{args: []string{"remove", "work"}, kind: "remove", ref: "work"},
		{args: []string{"status", "--json"}, kind: "status"},
		{args: []string{"help"}, kind: "help"},
		{args: []string{"--help"}, kind: "help"},
		{args: []string{"work"}, kind: "launch", ref: "work"},
		{args: []string{}, wantErr: true},
		{args: []string{"remove"}, wantErr: true}, // remove needs a ref
	}
	for _, c := range cases {
		cmd, err := route(c.args)
		if c.wantErr {
			if err == nil {
				t.Fatalf("args %v: want usage error", c.args)
			}
			continue
		}
		if err != nil {
			t.Fatalf("args %v: unexpected error %v", c.args, err)
		}
		if cmd.kind != c.kind {
			t.Fatalf("args %v: kind = %q, want %q", c.args, cmd.kind, c.kind)
		}
		if cmd.ref != c.ref {
			t.Fatalf("args %v: ref = %q, want %q", c.args, cmd.ref, c.ref)
		}
	}
}

func TestUnknownRefError(t *testing.T) {
	msg := unknownRefError("satatus").Error()
	if !strings.Contains(msg, "satatus") {
		t.Fatalf("message should name the input: %q", msg)
	}
	if !strings.Contains(msg, "command") || !strings.Contains(msg, "accounts") {
		t.Fatalf("message should mention it's neither a command nor account and point to `accounts`: %q", msg)
	}
}

func TestRouteAddAlias(t *testing.T) {
	cmd, err := route([]string{"add", "--alias", "work"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.alias != "work" {
		t.Fatalf("alias = %q, want work", cmd.alias)
	}
}

func TestRouteLaunchForwardsClaudeArgs(t *testing.T) {
	cmd, err := route([]string{"work", "--resume"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.kind != "launch" || cmd.ref != "work" {
		t.Fatalf("got %+v", cmd)
	}
	if len(cmd.claudeArgs) != 1 || cmd.claudeArgs[0] != "--resume" {
		t.Fatalf("claudeArgs = %v, want [--resume]", cmd.claudeArgs)
	}
}
