//go:build darwin

// Package supervise runs Claude Code as a child of switcch so a chat can be moved
// to another account mid-conversation: on a limit (or an intercepted prompt) it
// stops Claude and relaunches `claude --resume <session>` under the next
// account's profile. Transcripts are shared across profiles, so the same
// conversation continues.
package supervise

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/00mkp/switcch/internal/account"
	"github.com/00mkp/switcch/internal/config"
	"github.com/00mkp/switcch/internal/credsync"
	"github.com/00mkp/switcch/internal/hooks"
	"github.com/00mkp/switcch/internal/launch"
	"github.com/00mkp/switcch/internal/usage"
)

// Options configures one supervised session.
type Options struct {
	Pool       []account.Account // pool[0] is the starting account
	Config     config.Config
	ClaudeArgs []string
	RuntimeDir string // parent of the per-session signal dir ("" = default)
}

// ExitError carries Claude's exit status when it ended on its own, non-zero.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("claude exited with status %d", e.Code) }

// Test seams.
var (
	prepare = launch.Prepare
	usageOf = func(a account.Account) (usage.Usage, error) {
		u, _, err := usage.Cached(context.Background(), a)
		return u, err
	}
	captureBack            = func(a account.Account) { _, _ = credsync.Current(a) }
	command                = exec.Command
	claudePath             = func() (string, error) { return exec.LookPath("claude") }
	pollInterval           = 100 * time.Millisecond
	killGrace              = 5 * time.Second
	stderr       io.Writer = os.Stderr
)

func name(a account.Account) string {
	if a.Alias != "" {
		return a.Alias
	}
	return a.Email
}

func peak(u usage.Usage) float64 {
	return max(u.FiveHour.Utilization, u.SevenDay.Utilization)
}

// next returns the first pool account other than active whose usage is below
// limit (unknown usage counts as available), or nil. Requiring targets to be
// under the same line that triggered the switch prevents ping-ponging.
func next(pool []account.Account, active account.Account, usageOf func(account.Account) (usage.Usage, error), limit float64) *account.Account {
	for i := range pool {
		a := pool[i]
		if a.Slot == active.Slot {
			continue
		}
		if u, err := usageOf(a); err == nil && peak(u) >= limit {
			continue
		}
		return &pool[i]
	}
	return nil
}

type action int

const (
	stay action = iota
	switchAcct
	switchWithPrompt
	done
)

type event struct {
	kind      hooks.Kind
	exited    bool
	hasTarget bool
	mode      string
}

func decide(e event) action {
	switch {
	case e.exited:
		return done
	case e.kind == hooks.Intercept:
		// The prompt was already blocked by the hook: always relaunch so it is
		// delivered (on the target, or the same account if there is none).
		return switchWithPrompt
	case e.kind == hooks.RateLimited && e.hasTarget && e.mode == "auto":
		return switchAcct
	}
	return stay
}

// relaunchArgs keeps the user's own claude flags but replaces any session
// selection with `--resume <sid> <msg>` (or `--continue <msg>` with no sid).
func relaunchArgs(orig []string, sid, msg string) []string {
	var out []string
	for i := 0; i < len(orig); i++ {
		a := orig[i]
		switch a {
		case "--resume", "-r", "--session-id":
			if i+1 < len(orig) && !strings.HasPrefix(orig[i+1], "-") {
				i++
			}
			continue
		case "--continue", "-c", "--fork-session":
			continue
		}
		out = append(out, a)
	}
	if sid == "" {
		return append(out, "--continue", msg)
	}
	return append(out, "--resume", sid, msg)
}

func notice(tmpl string, from, to account.Account) string {
	return strings.NewReplacer("{from}", name(from), "{to}", name(to)).Replace(tmpl)
}

func childEnv(env []string, sigDir string) []string {
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if strings.HasPrefix(e, "CLAUDE_CODE_RETRY_WATCHDOG=") || strings.HasPrefix(e, "SWITCCH_SIGNAL_DIR=") {
			continue
		}
		out = append(out, e)
	}
	return append(out, "SWITCCH_SIGNAL_DIR="+sigDir)
}

// take reads and consumes one signal file.
func take(dir string, k hooks.Kind) (hooks.Signal, bool) {
	s, err := hooks.ReadSignal(dir, k)
	if err != nil {
		return hooks.Signal{}, false
	}
	_ = os.Remove(filepath.Join(dir, string(k)))
	return s, true
}

func clearMarkers(dir string) {
	for _, f := range []string{hooks.ArmedFile, hooks.BusyFile, string(hooks.RateLimited), string(hooks.Stopped), string(hooks.Intercept)} {
		_ = os.Remove(filepath.Join(dir, f))
	}
}

func signalDir(root string) (string, error) {
	if root == "" {
		base := os.Getenv("XDG_DATA_HOME")
		if base == "" {
			home, _ := os.UserHomeDir()
			base = filepath.Join(home, ".local", "share")
		}
		root = filepath.Join(base, "switcch", "runtime")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(root, fmt.Sprintf("sess-%d-", os.Getpid()))
}

func exitErr(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if c := ee.ExitCode(); c > 0 {
			return &ExitError{Code: c}
		}
		return nil
	}
	return err
}

func stopChild(cmd *exec.Cmd, exited <-chan error) {
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(killGrace):
		_ = cmd.Process.Kill()
		<-exited
	}
}

// Run supervises one chat until the user ends it.
func Run(ctx context.Context, o Options) error {
	if len(o.Pool) == 0 {
		return errors.New("supervise: empty account pool")
	}
	sigDir, err := signalDir(o.RuntimeDir)
	if err != nil {
		return err
	}
	defer os.RemoveAll(sigDir)
	claude, err := claudePath()
	if err != nil {
		return fmt.Errorf("claude not found in PATH: %w", err)
	}

	// Ctrl-C/quit belong to Claude. CATCH (not ignore) them: an ignored signal
	// would be inherited across exec and Claude would stop responding to Ctrl-C.
	quiet := make(chan os.Signal, 4)
	signal.Notify(quiet, os.Interrupt, syscall.SIGQUIT)
	defer signal.Stop(quiet)
	go func() {
		for range quiet {
		}
	}()
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(term)

	limit := 100.0
	active := o.Pool[0]
	args := o.ClaudeArgs
	for {
		_, env, err := prepare(active)
		if err != nil {
			return err
		}
		clearMarkers(sigDir)
		cmd := command(claude, args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		cmd.Env = childEnv(env, sigDir)
		if err := cmd.Start(); err != nil {
			return err
		}
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()

		var to account.Account
		var nextArgs []string
	watch:
		for {
			select {
			case err := <-exited:
				captureBack(active)
				return exitErr(err)
			case s := <-term:
				_ = cmd.Process.Signal(s)
				<-exited
				captureBack(active)
				return nil
			case <-ctx.Done():
				stopChild(cmd, exited)
				captureBack(active)
				return ctx.Err()
			case <-time.After(pollInterval):
				if s, ok := take(sigDir, hooks.Intercept); ok {
					tgt := next(o.Pool, active, usageOf, limit)
					to = active
					msg := s.Prompt
					if tgt != nil {
						to = *tgt
						msg = notice(o.Config.Notice, active, to) + "\n\n" + s.Prompt
					}
					if decide(event{kind: hooks.Intercept, hasTarget: tgt != nil, mode: o.Config.Mode}) == switchWithPrompt {
						nextArgs = relaunchArgs(o.ClaudeArgs, s.SessionID, msg)
						break watch
					}
				}
				if s, ok := take(sigDir, hooks.RateLimited); ok {
					tgt := next(o.Pool, active, usageOf, 100)
					if decide(event{kind: hooks.RateLimited, hasTarget: tgt != nil, mode: o.Config.Mode}) == switchAcct {
						to = *tgt
						nextArgs = relaunchArgs(o.ClaudeArgs, s.SessionID, notice(o.Config.Notice, active, to))
						break watch
					}
					// No viable account (or advisory): leave Claude showing its limit.
				}
				_, _ = take(sigDir, hooks.Stopped)
			}
		}

		fmt.Fprintf(stderr, "\nswitcch: moving this chat %s → %s…\n", name(active), name(to))
		stopChild(cmd, exited)
		captureBack(active)
		active = to
		args = nextArgs
	}
}
