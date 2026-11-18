//go:build darwin

// Command switcch runs multiple Claude accounts side by side: `switcch add`
// registers the logged-in account, `switcch <account>` launches an isolated
// Claude Code session for it. See docs/superpowers/specs for the design.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/00mkp/switcch/internal/account"
	"github.com/00mkp/switcch/internal/capture"
	"github.com/00mkp/switcch/internal/config"
	"github.com/00mkp/switcch/internal/hooks"
	"github.com/00mkp/switcch/internal/oauth"
	"github.com/00mkp/switcch/internal/session"
	"github.com/00mkp/switcch/internal/supervise"
	"github.com/00mkp/switcch/internal/usage"
)

// statusJSON emits the machine-readable status (the interop seam): per-account
// usage windows, model caps, and nested live sessions — and no secrets.
func statusJSON(accts []account.Account, sessions []session.Session, usageOf func(account.Account) (usage.Usage, error), limited func(account.Account) bool) ([]byte, error) {
	type jWindow struct {
		Utilization float64    `json:"utilization"`
		ResetsAt    *time.Time `json:"resetsAt,omitempty"`
	}
	type jModel struct {
		Model   string  `json:"model"`
		Percent float64 `json:"percent"`
		Active  bool    `json:"active"`
	}
	type jSession struct {
		PID       int       `json:"pid"`
		Cwd       string    `json:"cwd"`
		Started   time.Time `json:"started"`
		SessionID string    `json:"sessionId,omitempty"`
		Pool      []int     `json:"pool"`
	}
	type jAccount struct {
		Slot       int        `json:"slot"`
		Email      string     `json:"email"`
		Alias      string     `json:"alias,omitempty"`
		Limited    bool       `json:"limited"`
		FiveHour   *jWindow   `json:"fiveHour,omitempty"`
		SevenDay   *jWindow   `json:"sevenDay,omitempty"`
		Models     []jModel   `json:"models,omitempty"`
		UsageError string     `json:"usageError,omitempty"`
		Sessions   []jSession `json:"sessions"`
	}
	win := func(w usage.Window) *jWindow {
		jw := &jWindow{Utilization: w.Utilization}
		if w.HasReset {
			t := w.ResetsAt
			jw.ResetsAt = &t
		}
		return jw
	}
	out := struct {
		Schema   int        `json:"schema"`
		Accounts []jAccount `json:"accounts"`
	}{Schema: 1, Accounts: []jAccount{}}
	for _, a := range accts {
		ja := jAccount{Slot: a.Slot, Email: a.Email, Alias: a.Alias, Limited: limited(a), Sessions: []jSession{}}
		if u, err := usageOf(a); err == nil {
			ja.FiveHour = win(u.FiveHour)
			ja.SevenDay = win(u.SevenDay)
			for _, m := range u.Models {
				ja.Models = append(ja.Models, jModel{Model: m.Model, Percent: m.Percent, Active: m.Active})
			}
		} else {
			ja.UsageError = shortErr(err)
		}
		for _, s := range sessions {
			if s.Slot == a.Slot {
				pool := s.Pool
				if pool == nil {
					pool = []int{s.Slot}
				}
				ja.Sessions = append(ja.Sessions, jSession{PID: s.PID, Cwd: s.Cwd, Started: s.Started, SessionID: s.SessionID, Pool: pool})
			}
		}
		out.Accounts = append(out.Accounts, ja)
	}
	return json.MarshalIndent(out, "", "  ")
}

// renderList builds the `cswap list`-style tree: each account with its usage and
// its live sessions nested beneath. Pure, so it is testable without the network.
func renderList(accts []account.Account, sessions []session.Session, usageOf func(account.Account) (usage.Usage, error)) string {
	var b strings.Builder
	for _, a := range accts {
		label := a.Email
		if a.Alias != "" {
			label += " (" + a.Alias + ")"
		}
		fmt.Fprintf(&b, "%d: %s\n", a.Slot, label)
		if u, err := usageOf(a); err == nil {
			fmt.Fprintf(&b, "   5h: %3.0f%%   7d: %3.0f%%\n", u.FiveHour.Utilization, u.SevenDay.Utilization)
			for _, m := range u.Models {
				if m.Active {
					fmt.Fprintf(&b, "   %s: %3.0f%%\n", m.Model, m.Percent)
				}
			}
		} else {
			fmt.Fprintf(&b, "   usage unavailable (%s) — try `switcch add` to re-register\n", shortErr(err))
		}
		for _, s := range sessions {
			if s.Slot == a.Slot {
				fmt.Fprintf(&b, "   ● session pid %d  %s\n", s.PID, s.Cwd)
			}
		}
	}
	return b.String()
}

// shortErr trims a wrapped error to its most actionable tail.
func shortErr(err error) string {
	msg := err.Error()
	if errors.Is(err, oauth.ErrInvalidGrant) {
		return "credential expired/rotated"
	}
	if i := strings.LastIndex(msg, ": "); i >= 0 {
		return msg[i+2:]
	}
	return msg
}

// friendlyLaunchError turns a rotated/expired-credential failure into actionable
// guidance; other errors pass through unchanged.
func friendlyLaunchError(ref string, err error) error {
	if errors.Is(err, oauth.ErrInvalidGrant) {
		return fmt.Errorf("the stored credential for %q has expired or been rotated; re-register it with `switcch add` while logged in as that account", ref)
	}
	return err
}

type command struct {
	kind       string // "add" | "accounts" | "list" | "remove" | "status" | "help" | "launch" | "fallback" | "config-set" | "config-show"
	ref        string
	alias      string
	claudeArgs []string

	// launch failover overrides (empty = use account/config defaults)
	pin       bool
	fallback  []string
	trigger   string
	threshold string
	mode      string

	// fallback command
	refs  []string
	clear bool

	// config set
	key, value string
}

// parseLaunch splits `<account> [switcch flags] [-- claude args]`. switcch's own
// flags are picked off; anything else (and everything after `--`) goes to claude.
func parseLaunch(args []string) (command, error) {
	c := command{kind: "launch", ref: args[0]}
	rest := args[1:]
	takeValue := func(i int, flag string) (string, error) {
		if i+1 >= len(rest) {
			return "", fmt.Errorf("%s needs a value", flag)
		}
		return rest[i+1], nil
	}
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch a {
		case "--":
			c.claudeArgs = append(c.claudeArgs, rest[i+1:]...)
			return c, nil
		case "--pin":
			c.pin = true
		case "--fallback", "--threshold", "--trigger", "--mode":
			v, err := takeValue(i, a)
			if err != nil {
				return command{}, err
			}
			switch a {
			case "--fallback":
				c.fallback = strings.Split(v, ",")
			case "--threshold":
				c.threshold = v
			case "--trigger":
				c.trigger = v
			case "--mode":
				c.mode = v
			}
			i++
		default:
			c.claudeArgs = append(c.claudeArgs, a)
		}
	}
	return c, nil
}

// route classifies CLI args into a command without performing any side effects.
func route(args []string) (command, error) {
	if len(args) == 0 {
		return command{}, errors.New("usage: switcch <account|add|accounts|list|status|remove|help> — run `switcch help` for details")
	}
	switch args[0] {
	case "add":
		c := command{kind: "add"}
		rest := args[1:]
		for i := 0; i < len(rest); i++ {
			if rest[i] == "--alias" && i+1 < len(rest) {
				c.alias = rest[i+1]
				i++
			}
		}
		return c, nil
	case "accounts":
		return command{kind: "accounts"}, nil
	case "list":
		return command{kind: "list"}, nil
	case "remove":
		if len(args) < 2 {
			return command{}, fmt.Errorf("usage: switcch remove <account>")
		}
		return command{kind: "remove", ref: args[1]}, nil
	case "status":
		return command{kind: "status"}, nil
	case "help", "-h", "--help":
		return command{kind: "help"}, nil
	case "fallback":
		if len(args) < 2 {
			return command{}, errors.New("usage: switcch fallback <account> <account>... | switcch fallback <account> --clear")
		}
		c := command{kind: "fallback", ref: args[1]}
		for _, a := range args[2:] {
			if a == "--clear" {
				c.clear = true
				continue
			}
			c.refs = append(c.refs, a)
		}
		if !c.clear && len(c.refs) == 0 {
			return command{}, errors.New("usage: switcch fallback <account> <account>... | switcch fallback <account> --clear")
		}
		return c, nil
	case "version":
		return command{kind: "version"}, nil
	case "setup":
		return command{kind: "setup"}, nil
	case "uninstall-hooks":
		return command{kind: "uninstall-hooks"}, nil
	case "config":
		if len(args) >= 2 && args[1] == "show" {
			return command{kind: "config-show"}, nil
		}
		if len(args) == 4 && args[1] == "set" {
			return command{kind: "config-set", key: args[2], value: args[3]}, nil
		}
		return command{}, errors.New("usage: switcch config show | switcch config set <key> <value>")
	default:
		return parseLaunch(args)
	}
}

const usageText = `switcch — run multiple Claude accounts side by side.

Usage:
  switcch <account>           launch an isolated Claude Code session for an account
                              (by alias, email, or slot number); extra args pass to claude
  switcch add [--alias NAME]  register the currently logged-in account
  switcch accounts            list registered accounts
  switcch list                accounts with usage (5h/7d) and live sessions
  switcch status --json       machine-readable status
  switcch remove <account>    forget an account (deletes its stored credential)
  switcch fallback <account> <account>...
                              set where an account's sessions fail over to (once)
  switcch fallback <account> --clear
                              pin an account (no failover)
  switcch setup               install switcch's hooks into ~/.claude/settings.json
  switcch uninstall-hooks     remove exactly the hooks switcch installed
  switcch config show | set <key> <value>
                              trigger (proactive|reactive), threshold (1-100),
                              mode (auto|advisory), idleMigrateMinutes,
                              waitForReset (true|false), modelFallback (a,b | ""),
                              eventCommand, notice, modelNotice, resumeMessage
  switcch version             print the version
  switcch help                show this help

Launch flags (override for one session):
  --pin  --fallback a,b  --trigger T  --threshold N  --mode M   (-- passes the rest to claude)`

// version is set at build time: -ldflags "-X main.version=v1.0.0".
var version string

// versionString picks the build-time version, else the module version that
// `go install …@vX` records, else "dev".
func versionString(info *debug.BuildInfo, ldflag string) string {
	if ldflag != "" {
		return ldflag
	}
	if info != nil && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

// configText renders `switcch config show`.
func configText(c config.Config) string {
	ev := c.EventCommand
	if ev == "" {
		ev = "(off)"
	}
	mf := strings.Join(c.ModelFallback, ",")
	if mf == "" {
		mf = "(off)"
	}
	return fmt.Sprintf("trigger            %s\nthreshold          %g%%\nmode               %s\nidleMigrateMinutes %d\nwaitForReset       %t\nmodelFallback      %s\neventCommand       %s\nnotice             %s\nmodelNotice        %s\nresumeMessage      %s\n",
		c.Trigger, c.Threshold, c.Mode, c.IdleMigrateMinutes, c.WaitForReset, mf, ev, c.Notice, c.ModelNotice, c.ResumeMessage)
}

// hookPaths returns the user's real ~/.claude/settings.json and the absolute
// path of this switcch binary (what the installed hooks invoke).
func hookPaths() (settings, exe string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	exe, err = os.Executable()
	if err != nil {
		return "", "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), hookExe(exe, exec.LookPath), nil
}

// hookExe is the path the hooks invoke. It prefers the `switcch` found on PATH
// when that is this same binary (e.g. /opt/homebrew/bin/switcch → Cellar/…),
// because a resolved, versioned path disappears on upgrade; otherwise it is
// os.Executable() as-is (never symlink-resolved, for the same reason).
func hookExe(exe string, lookPath func(string) (string, error)) string {
	p, err := lookPath("switcch")
	if err != nil {
		return exe
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	a, err1 := os.Stat(p)
	b, err2 := os.Stat(exe)
	if err1 == nil && err2 == nil && os.SameFile(a, b) {
		return p
	}
	return exe
}

// unknownRefError explains that a first argument matched neither a command nor a
// registered account (e.g. a mistyped subcommand).
func unknownRefError(ref string) error {
	return fmt.Errorf("%q is not a switcch command or a registered account — run `switcch help` for commands, or `switcch accounts` to list accounts", ref)
}

func run(args []string) error {
	cmd, err := route(args)
	if err != nil {
		return err
	}
	store, err := account.Load()
	if err != nil {
		return err
	}
	switch cmd.kind {
	case "add":
		a, err := capture.Capture(store, cmd.alias)
		if err != nil {
			return err
		}
		if err := store.Save(); err != nil {
			return err
		}
		label := ""
		if a.Alias != "" {
			label = " (" + a.Alias + ")"
		}
		fmt.Printf("Added account %d: %s%s\n", a.Slot, a.Email, label)
		return nil
	case "accounts":
		accts := store.List()
		if len(accts) == 0 {
			fmt.Println("No accounts registered. Run `switcch add` while logged in to Claude Code.")
			return nil
		}
		for _, a := range accts {
			label := ""
			if a.Alias != "" {
				label = " (" + a.Alias + ")"
			}
			fb := ""
			if pool := store.Pool(a); len(pool) > 1 {
				var names []string
				for _, f := range pool[1:] {
					n := f.Alias
					if n == "" {
						n = f.Email
					}
					names = append(names, n)
				}
				fb = "  → fallback: " + strings.Join(names, ", ")
			}
			fmt.Printf("  %d: %s%s%s\n", a.Slot, a.Email, label, fb)
		}
		return nil
	case "list":
		accts := store.List()
		if len(accts) == 0 {
			fmt.Println("No accounts registered. Run `switcch add` while logged in to Claude Code.")
			return nil
		}
		sessions, _ := session.List()
		fmt.Print(renderList(accts, sessions, func(a account.Account) (usage.Usage, error) {
			u, _, err := usage.Cached(context.Background(), a)
			return u, err
		}))
		return nil
	case "remove":
		a, err := capture.Forget(store, cmd.ref)
		if err != nil {
			return err
		}
		if err := store.Save(); err != nil {
			return err
		}
		fmt.Printf("Removed account %d: %s\n", a.Slot, a.Email)
		return nil
	case "status":
		accts := store.List()
		sessions, _ := session.List()
		b, err := statusJSON(accts, sessions, func(a account.Account) (usage.Usage, error) {
			u, _, err := usage.Cached(context.Background(), a)
			return u, err
		}, supervise.LimitedRecently)
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	case "help":
		fmt.Println(usageText)
		return nil
	case "fallback":
		if cmd.clear {
			cmd.refs = nil
		}
		if err := store.SetFallback(cmd.ref, cmd.refs); err != nil {
			return err
		}
		if err := store.Save(); err != nil {
			return err
		}
		if cmd.clear {
			fmt.Printf("%s is now pinned (no fallback)\n", cmd.ref)
		} else {
			fmt.Printf("%s now falls back to: %s\n", cmd.ref, strings.Join(cmd.refs, ", "))
		}
		return nil
	case "version":
		info, _ := debug.ReadBuildInfo()
		fmt.Println("switcch " + versionString(info, version))
		return nil
	case "setup":
		p, exe, err := hookPaths()
		if err != nil {
			return err
		}
		if err := hooks.Install(p, exe); err != nil {
			return err
		}
		fmt.Printf("Installed switcch hooks (%s) in %s\n", strings.Join(hooks.Events, ", "), p)
		return nil
	case "uninstall-hooks":
		p, _, err := hookPaths()
		if err != nil {
			return err
		}
		if err := hooks.Uninstall(p); err != nil {
			return err
		}
		fmt.Printf("Removed switcch hooks from %s\n", p)
		return nil
	case "config-set":
		if err := config.Set(cmd.key, cmd.value); err != nil {
			return err
		}
		fmt.Printf("%s = %s\n", cmd.key, cmd.value)
		return nil
	case "config-show":
		c, err := config.Load()
		if err != nil {
			return err
		}
		fmt.Print(configText(c))
		return nil
	case "launch":
		a, err := store.Resolve(cmd.ref)
		if err != nil {
			if errors.Is(err, account.ErrNoAccount) {
				return unknownRefError(cmd.ref)
			}
			return err
		}
		pool, err := resolvePool(store, a, cmd)
		if err != nil {
			return err
		}
		cfg, err := effectiveConfig(cmd)
		if err != nil {
			return err
		}
		if p, exe, err := hookPaths(); err == nil {
			if err := hooks.Install(p, exe); err != nil {
				fmt.Fprintf(os.Stderr, "switcch: warning: could not install hooks (%v) — this chat runs without automatic failover\n", err)
			}
		}
		err = supervise.Run(context.Background(), supervise.Options{Pool: pool, Config: cfg, ClaudeArgs: cmd.claudeArgs})
		return friendlyLaunchError(cmd.ref, err)
	}
	return nil
}

// resolvePool is the session's account pool: the account plus its configured
// fallback, unless --pin (just the account) or --fallback (override list).
func resolvePool(store *account.Store, a account.Account, cmd command) ([]account.Account, error) {
	if cmd.pin {
		return []account.Account{a}, nil
	}
	if cmd.fallback == nil {
		return store.Pool(a), nil
	}
	pool := []account.Account{a}
	for _, r := range cmd.fallback {
		f, err := store.Resolve(strings.TrimSpace(r))
		if err != nil {
			return nil, fmt.Errorf("--fallback %q: %w", r, err)
		}
		if f.Slot != a.Slot {
			pool = append(pool, f)
		}
	}
	return pool, nil
}

// effectiveConfig is the saved config with this launch's flag overrides applied.
func effectiveConfig(cmd command) (config.Config, error) {
	c, err := config.Load()
	if err != nil {
		return c, err
	}
	for _, kv := range [][2]string{{"trigger", cmd.trigger}, {"threshold", cmd.threshold}, {"mode", cmd.mode}} {
		if kv[1] == "" {
			continue
		}
		if err := config.Validate(&c, kv[0], kv[1]); err != nil {
			return c, fmt.Errorf("--%s: %w", kv[0], err)
		}
	}
	return c, nil
}

func main() {
	// Hook entry point: dispatched before anything else so it stays fast and is
	// inert (exit 0) outside switcch sessions.
	if len(os.Args) >= 3 && os.Args[1] == "__hook" {
		code, msg := hooks.HandleHook(os.Args[2], os.Stdin, os.Getenv)
		if msg != "" {
			fmt.Fprintln(os.Stderr, msg)
		}
		os.Exit(code)
	}
	err := run(os.Args[1:])
	var ee *supervise.ExitError
	if errors.As(err, &ee) {
		os.Exit(ee.Code) // Claude's own exit status; it already printed why
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "switcch: "+err.Error())
		os.Exit(1)
	}
}
