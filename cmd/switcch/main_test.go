//go:build darwin

package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/00mkp/switcch/internal/account"
	"github.com/00mkp/switcch/internal/oauth"
	"github.com/00mkp/switcch/internal/session"
	"github.com/00mkp/switcch/internal/usage"
)

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
