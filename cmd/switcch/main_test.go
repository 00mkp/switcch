//go:build darwin

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	usageOf := func(account.Account) (usage.Usage, error) {
		return usage.Usage{
			FiveHour: usage.Window{Utilization: 7},
			SevenDay: usage.Window{Utilization: 11},
			Models:   []usage.ModelCap{{Model: "Fable", Percent: 100, Active: true}},
		}, nil
	}
	b, err := statusJSON(accts, sessions, usageOf, func(account.Account) bool { return false })
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
	usageOf := func(a account.Account) (usage.Usage, error) {
		if a.Slot == 1 {
			return usage.Usage{}, errors.New("token expired") // personal: usage unavailable
		}
		return usage.Usage{FiveHour: usage.Window{Utilization: 3}, SevenDay: usage.Window{Utilization: 5}}, nil
	}
	out := renderList(accts, sessions, usageOf)

	if !strings.Contains(out, "personal") || !strings.Contains(out, "work") {
		t.Fatalf("missing account labels:\n%s", out)
	}
	// personal's usage failed — it must say so, not silently show nothing.
	if !strings.Contains(out, "unavailable") {
		t.Fatalf("personal usage failure should surface 'unavailable':\n%s", out)
	}
	// work shows its numbers and its session.
	if !strings.Contains(out, "3%") || !strings.Contains(out, "5h") {
		t.Fatalf("missing work usage numbers:\n%s", out)
	}
	if !strings.Contains(out, "/w") || !strings.Contains(out, "123") {
		t.Fatalf("missing the work session:\n%s", out)
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

func TestRouteLaunchFailoverFlags(t *testing.T) {
	c, err := route([]string{"work", "--fallback", "other,personal", "--threshold", "80", "--trigger", "reactive", "--mode", "advisory", "--", "--resume"})
	if err != nil {
		t.Fatal(err)
	}
	if c.kind != "launch" || c.ref != "work" {
		t.Fatalf("got %+v", c)
	}
	if len(c.fallback) != 2 || c.fallback[0] != "other" || c.fallback[1] != "personal" {
		t.Fatalf("fallback override = %v", c.fallback)
	}
	if c.threshold != "80" || c.trigger != "reactive" || c.mode != "advisory" {
		t.Fatalf("flags = %+v", c)
	}
	if len(c.claudeArgs) != 1 || c.claudeArgs[0] != "--resume" {
		t.Fatalf("claudeArgs = %v", c.claudeArgs)
	}
	p, _ := route([]string{"work", "--pin", "--model", "opus"})
	if !p.pin {
		t.Fatal("--pin not parsed")
	}
	if len(p.claudeArgs) != 2 || p.claudeArgs[0] != "--model" {
		t.Fatalf("unknown flags should pass through to claude, got %v", p.claudeArgs)
	}
	if _, err := route([]string{"work", "--threshold"}); err == nil {
		t.Fatal("--threshold without a value must error")
	}
}

func TestRouteFallbackAndConfig(t *testing.T) {
	c, err := route([]string{"fallback", "work", "personal", "other"})
	if err != nil || c.kind != "fallback" || c.ref != "work" || len(c.refs) != 2 || c.clear {
		t.Fatalf("fallback set: %+v %v", c, err)
	}
	c, err = route([]string{"fallback", "work", "--clear"})
	if err != nil || !c.clear {
		t.Fatalf("fallback clear: %+v %v", c, err)
	}
	if _, err := route([]string{"fallback", "work"}); err == nil {
		t.Fatal("fallback with no targets and no --clear must error")
	}
	c, err = route([]string{"config", "set", "threshold", "85"})
	if err != nil || c.kind != "config-set" || c.key != "threshold" || c.value != "85" {
		t.Fatalf("config set: %+v %v", c, err)
	}
	c, err = route([]string{"config", "show"})
	if err != nil || c.kind != "config-show" {
		t.Fatalf("config show: %+v %v", c, err)
	}
}

func TestRouteSetupAndUninstallHooks(t *testing.T) {
	if c, err := route([]string{"setup"}); err != nil || c.kind != "setup" {
		t.Fatalf("setup: %+v %v", c, err)
	}
	if c, err := route([]string{"uninstall-hooks"}); err != nil || c.kind != "uninstall-hooks" {
		t.Fatalf("uninstall-hooks: %+v %v", c, err)
	}
}

func TestResolvePool(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s, _ := account.Load()
	s.Add(account.Account{Email: "p@x.com", Alias: "personal"})
	s.Add(account.Account{Email: "w@x.com", Alias: "work"})
	s.Add(account.Account{Email: "o@x.com", Alias: "other"})
	_ = s.SetFallback("work", []string{"personal"})
	w, _ := s.Resolve("work")

	names := func(p []account.Account) string {
		var n []string
		for _, a := range p {
			n = append(n, a.Alias)
		}
		return strings.Join(n, ",")
	}
	if p, _ := resolvePool(s, w, command{}); names(p) != "work,personal" {
		t.Fatalf("configured pool = %s", names(p))
	}
	if p, _ := resolvePool(s, w, command{pin: true}); names(p) != "work" {
		t.Fatalf("--pin pool = %s", names(p))
	}
	if p, _ := resolvePool(s, w, command{fallback: []string{"other"}}); names(p) != "work,other" {
		t.Fatalf("--fallback override pool = %s", names(p))
	}
	if _, err := resolvePool(s, w, command{fallback: []string{"nope"}}); err == nil {
		t.Fatal("unknown --fallback account must error")
	}
}

func TestEffectiveConfigAppliesFlagOverrides(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c, err := effectiveConfig(command{threshold: "75", trigger: "reactive"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Threshold != 75 || c.Trigger != "reactive" || c.Mode != "auto" {
		t.Fatalf("effective config = %+v", c)
	}
	if _, err := effectiveConfig(command{threshold: "500"}); err == nil {
		t.Fatal("invalid --threshold must error")
	}
}

func TestHookExePrefersStablePathEntry(t *testing.T) {
	d := t.TempDir()
	cellar := filepath.Join(d, "cellar", "1.0", "switcch")
	bin := filepath.Join(d, "bin", "switcch")
	other := filepath.Join(d, "other", "switcch")
	for _, p := range []string{cellar, other} {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(p), 0o755)
	}
	_ = os.MkdirAll(filepath.Dir(bin), 0o755)
	if err := os.Symlink(cellar, bin); err != nil {
		t.Fatal(err)
	}
	look := func(p string) func(string) (string, error) {
		return func(string) (string, error) { return p, nil }
	}
	if got := hookExe(cellar, look(bin)); got != bin {
		t.Fatalf("PATH entry resolving to this binary should win: %q", got)
	}
	if got := hookExe(cellar, look(other)); got != cellar {
		t.Fatalf("an unrelated PATH switcch must not be used: %q", got)
	}
	if got := hookExe(bin, func(string) (string, error) { return "", errors.New("nope") }); got != bin {
		t.Fatalf("without a PATH match the exe is used unresolved: %q", got)
	}
}

func TestStatusJSONSchema(t *testing.T) {
	accts := []account.Account{{Slot: 1, Email: "w@x.com", Alias: "work"}, {Slot: 2, Email: "p@x.com"}}
	sessions := []session.Session{{Slot: 2, PID: 7, Cwd: "/c", SessionID: "sid-7", Pool: []int{2, 1}}}
	usageOf := func(a account.Account) (usage.Usage, error) {
		if a.Slot == 2 {
			return usage.Usage{}, errors.New("401")
		}
		return usage.Usage{FiveHour: usage.Window{Utilization: 42}}, nil
	}
	limited := func(a account.Account) bool { return a.Slot == 1 }
	b, err := statusJSON(accts, sessions, usageOf, limited)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["schema"] != float64(1) {
		t.Fatalf("schema = %v", m["schema"])
	}
	accs := m["accounts"].([]any)
	a0, a1 := accs[0].(map[string]any), accs[1].(map[string]any)
	if a0["limited"] != true || a1["limited"] != false {
		t.Fatalf("limited flags: %v %v", a0["limited"], a1["limited"])
	}
	if s, ok := a0["sessions"].([]any); !ok || len(s) != 0 {
		t.Fatalf("an account with no sessions must have sessions: [], got %#v", a0["sessions"])
	}
	if a1["usageError"] == nil {
		t.Fatal("usage failure must be reported")
	}
	s := a1["sessions"].([]any)[0].(map[string]any)
	if s["sessionId"] != "sid-7" || len(s["pool"].([]any)) != 2 {
		t.Fatalf("session must carry sessionId and pool even when usage failed: %v", s)
	}
}
