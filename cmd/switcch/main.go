//go:build darwin

// Command switcch runs multiple Claude accounts side by side: `switcch add`
// registers the logged-in account, `switcch <account>` launches an isolated
// Claude Code session for it. See docs/superpowers/specs for the design.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/00mkp/switcch/internal/account"
	"github.com/00mkp/switcch/internal/capture"
	"github.com/00mkp/switcch/internal/launch"
	"github.com/00mkp/switcch/internal/oauth"
	"github.com/00mkp/switcch/internal/session"
	"github.com/00mkp/switcch/internal/usage"
)

// renderList builds the `cswap list`-style tree: each account with its usage and
// its live sessions nested beneath. Pure, so it is testable without the network.
func renderList(accts []account.Account, sessions []session.Session, usageOf func(account.Account) (usage.Usage, bool)) string {
	var b strings.Builder
	for _, a := range accts {
		label := a.Email
		if a.Alias != "" {
			label += " (" + a.Alias + ")"
		}
		fmt.Fprintf(&b, "%d: %s\n", a.Slot, label)
		if u, ok := usageOf(a); ok {
			fmt.Fprintf(&b, "   5h: %3.0f%%   7d: %3.0f%%\n", u.FiveHour.Utilization, u.SevenDay.Utilization)
			for _, m := range u.Models {
				fmt.Fprintf(&b, "   %s: %3.0f%%\n", m.Model, m.Percent)
			}
		}
		for _, s := range sessions {
			if s.Slot == a.Slot {
				fmt.Fprintf(&b, "   ● session pid %d  %s\n", s.PID, s.Cwd)
			}
		}
	}
	return b.String()
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
		return command{}, fmt.Errorf("usage: switcch <account> | add [--alias NAME] | accounts | remove <account>")
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
	default:
		return command{kind: "launch", ref: args[0], claudeArgs: args[1:]}, nil
	}
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
		fmt.Print(renderList(accts, sessions, func(a account.Account) (usage.Usage, bool) {
			u, _, err := usage.Cached(context.Background(), a)
			if err != nil {
				return usage.Usage{}, false
			}
			return u, true
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
	case "launch":
		a, err := store.Resolve(cmd.ref)
		if err != nil {
			return fmt.Errorf("%w (see `switcch accounts`)", err)
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
