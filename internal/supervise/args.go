//go:build darwin

package supervise

import (
	"regexp"
	"strings"
)

// arity is how many values a claude option takes.
type arity int

const (
	none     arity = iota // --verbose
	required              // --model <model>
	optional              // --resume [value]
	variadic              // --add-dir <directories...>
)

// optSpec maps every claude option spelling to its arity.
type optSpec map[string]arity

// An option line in `claude --help`: exactly two leading spaces (wrapped
// description lines are indented much further).
var helpLine = regexp.MustCompile(`^  (-[\w-]+(?:, -[\w-]+)*)(?: ([<\[])([^>\]]*)[>\]])?`)

// parseHelp reads claude's option table from its --help text, so relaunching
// can tell an option's value from the user's positional prompt without
// hard-coding a list that drifts with each Claude Code release.
func parseHelp(text string) optSpec {
	sp := optSpec{}
	for _, line := range strings.Split(text, "\n") {
		m := helpLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ar := none
		switch {
		case m[2] == "":
		case strings.HasSuffix(m[3], "..."):
			ar = variadic
		case m[2] == "[":
			ar = optional
		default:
			ar = required
		}
		for _, n := range strings.Split(m[1], ", ") {
			if _, seen := sp[n]; !seen {
				sp[n] = ar
			}
		}
	}
	return sp
}

// sessionFlags select which conversation to open; a relaunch replaces them.
var sessionFlags = map[string]bool{"--resume": true, "-r": true, "--session-id": true, "--continue": true, "-c": true, "--fork-session": true}

// relaunchArgs keeps the user's own claude options (replacing their --model
// with model when model != "") but drops their positional
// prompt (it was already sent — replaying it would re-run it) and any session
// selection, then appends `--resume <sid> -- <msg>` (`--continue -- <msg>`
// with no sid). "--" stops a message that starts with "-" from being parsed as
// an option. With no option table (sp nil) nothing of the user's args can be
// told apart safely, so only the resume is kept.
func relaunchArgs(orig []string, sp optSpec, model, sid, msg string) []string {
	var out []string
	for i := 0; sp != nil && i < len(orig); i++ {
		a := orig[i]
		if a == "--" {
			break // the rest is positional
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			continue // the user's prompt
		}
		name, _, inline := strings.Cut(a, "=")
		take := []string{a}
		if !inline {
			switch sp[name] {
			case required:
				if i+1 < len(orig) {
					i++
					take = append(take, orig[i])
				}
			case optional:
				if i+1 < len(orig) && !strings.HasPrefix(orig[i+1], "-") {
					i++
					take = append(take, orig[i])
				}
			case variadic:
				for i+1 < len(orig) && !strings.HasPrefix(orig[i+1], "-") {
					i++
					take = append(take, orig[i])
				}
			}
		}
		if !sessionFlags[name] && !(model != "" && name == "--model") {
			out = append(out, take...)
		}
	}
	if model != "" {
		out = append(out, "--model", model)
	}
	if sid == "" {
		return append(out, "--continue", "--", msg)
	}
	return append(out, "--resume", sid, "--", msg)
}

// resumeID is the session the user asked to resume (--resume/-r/--session-id <id>), if any.
func resumeID(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--resume", "-r", "--session-id":
			if !strings.HasPrefix(args[i+1], "-") {
				return args[i+1]
			}
		}
	}
	return ""
}
