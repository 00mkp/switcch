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
	"strings"
	"time"

	"github.com/00mkp/switcch/internal/account"
	"github.com/00mkp/switcch/internal/capture"
	"github.com/00mkp/switcch/internal/launch"
	"github.com/00mkp/switcch/internal/oauth"
	"github.com/00mkp/switcch/internal/session"
	"github.com/00mkp/switcch/internal/usage"
)

// statusJSON emits the machine-readable status (the interop seam): per-account
// usage windows, model caps, and nested live sessions — and no secrets.
func statusJSON(accts []account.Account, sessions []session.Session, usageOf func(account.Account) (usage.Usage, error)) ([]byte, error) {
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
		PID     int       `json:"pid"`
		Cwd     string    `json:"cwd"`
		Started time.Time `json:"started"`
	}
	type jAccount struct {
		Slot       int        `json:"slot"`
		Email      string     `json:"email"`
		Alias      string     `json:"alias,omitempty"`
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
		Accounts []jAccount `json:"accounts"`
	}{}
	for _, a := range accts {
		ja := jAccount{Slot: a.Slot, Email: a.Email, Alias: a.Alias, Sessions: []jSession{}}
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
				ja.Sessions = append(ja.Sessions, jSession{PID: s.PID, Cwd: s.Cwd, Started: s.Started})
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
	kind       string // "add" | "list" | "remove" | "launch"
	ref        string
	alias      string
	claudeArgs []string
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
	default:
		return command{kind: "launch", ref: args[0], claudeArgs: args[1:]}, nil
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
  switcch help                show this help`

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
			fmt.Printf("  %d: %s%s\n", a.Slot, a.Email, label)
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
		})
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	case "help":
		fmt.Println(usageText)
		return nil
	case "launch":
		a, err := store.Resolve(cmd.ref)
		if err != nil {
			if errors.Is(err, account.ErrNoAccount) {
				return unknownRefError(cmd.ref)
			}
			return err
		}
		dir, env, err := launch.Prepare(a)
		if err != nil {
			return friendlyLaunchError(cmd.ref, err)
		}
		return launch.Exec(dir, env, cmd.claudeArgs)
	}
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "switcch: "+err.Error())
		os.Exit(1)
	}
}
