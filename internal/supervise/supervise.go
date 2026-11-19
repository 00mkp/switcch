//go:build darwin

// Package supervise runs Claude Code as a child of switcch so a chat can be moved
// to another account mid-conversation: on a limit (or an intercepted prompt) it
// stops Claude and relaunches `claude --resume <session>` under the next
// account's profile. Transcripts are shared across profiles, so the same
// conversation continues.
package supervise

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/00mkp/switcch/internal/account"
	"github.com/00mkp/switcch/internal/config"
	"github.com/00mkp/switcch/internal/credsync"
	"github.com/00mkp/switcch/internal/event"
	"github.com/00mkp/switcch/internal/hooks"
	"github.com/00mkp/switcch/internal/launch"
	"github.com/00mkp/switcch/internal/session"
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
	captureBack             = func(a account.Account) { _, _ = credsync.Current(a) }
	command                 = exec.Command
	claudePath              = func() (string, error) { return exec.LookPath("claude") }
	claudeHelp              = runHelp
	childrenOf              = pgrepChildren
	recordSession           = session.Record
	emitEvent               = event.Emit
	pollInterval            = 100 * time.Millisecond
	killGrace               = 5 * time.Second
	stderr        io.Writer = os.Stderr

	usagePollInterval = 60 * time.Second // proactive: how often the active account's usage is checked
	notify            = macNotify        // in-session messages (stderr would garble Claude's screen)
	clock             = time.Now         // idle-time measurement
	// markerRoot is where cross-chat "account hit a hard limit" markers live
	// (shared by every switcch session on this machine).
	markerRoot = func(root string) string { return root }
	limitedTTL = 30 * time.Minute
	resetGrace = 30 * time.Second // wait-for-reset resumes this long after the reset
	// earlyExit: a relaunched Claude that dies this soon gets its carried
	// message printed, so it is never silently lost.
	earlyExit = 15 * time.Second
)

func runHelp(bin string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--help").Output()
	return string(out), err
}

func pgrepChildren(pid int) []int {
	out, _ := exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output() // exit 1 = none
	var kids []int
	for _, f := range strings.Fields(string(out)) {
		if n, err := strconv.Atoi(f); err == nil {
			kids = append(kids, n)
		}
	}
	return kids
}

func macNotify(title, msg string) {
	q := func(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }
	_ = exec.Command("osascript", "-e", "display notification "+q(msg)+" with title "+q(title)).Run()
}

// limitedMarker is touched when a chat on a hits a hard limit, so other chats
// on a arm themselves without waiting for their own usage poll, and no chat
// moves back onto a for a while.
func limitedMarker(root string, a account.Account) string {
	return filepath.Join(root, fmt.Sprintf("limited-%d", a.Slot))
}

func markLimited(root string, a account.Account) {
	_ = os.WriteFile(limitedMarker(root, a), nil, 0o600)
	now := time.Now()
	_ = os.Chtimes(limitedMarker(root, a), now, now)
}

func limitedRecently(root string, a account.Account) bool {
	fi, err := os.Stat(limitedMarker(root, a))
	return err == nil && time.Since(fi.ModTime()) < limitedTTL
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func name(a account.Account) string {
	if a.Alias != "" {
		return a.Alias
	}
	return a.Email
}

func peak(u usage.Usage) float64 {
	return max(u.FiveHour.Utilization, u.SevenDay.Utilization)
}

// next returns the first pool account other than active that isn't skipped and
// whose usage is below limit (unknown usage counts as available), or nil.
// Requiring targets to be under the same line that triggered the switch, and
// skipping accounts that recently hit a hard limit, prevents ping-ponging.
func next(pool []account.Account, active account.Account, usageOf func(account.Account) (usage.Usage, error), limit float64, skip func(account.Account) bool) *account.Account {
	for i := range pool {
		a := pool[i]
		if a.Slot == active.Slot || skip(a) {
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

type hookEvent struct {
	kind      hooks.Kind
	exited    bool
	hasTarget bool
	mode      string
}

func decide(e hookEvent) action {
	switch {
	case e.exited:
		return done
	case e.kind == hooks.Intercept:
		// The prompt was already blocked by the hook: always relaunch so it is
		// delivered (on the target, or the same account if there is none).
		return switchWithPrompt
	case e.kind == hooks.RateLimited && e.hasTarget && e.mode != "advisory":
		return switchAcct
	}
	return stay
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
	for _, f := range []string{hooks.ArmedFile, hooks.BusyFile, hooks.ChildPIDFile, string(hooks.RateLimited), string(hooks.Stopped), string(hooks.Intercept)} {
		_ = os.Remove(filepath.Join(dir, f))
	}
}

// defaultRuntimeRoot is $XDG_DATA_HOME/switcch/runtime (signal dirs + markers).
func defaultRuntimeRoot() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "switcch", "runtime")
}

// LimitedRecently reports whether a switcch chat on a hit a hard limit within
// the marker window (status --json "limited").
func LimitedRecently(a account.Account) bool {
	return limitedRecently(markerRoot(defaultRuntimeRoot()), a)
}

// signalDir creates this supervisor's signal dir under root (default
// defaultRuntimeRoot) and returns both.
func signalDir(root string) (dir, resolvedRoot string, err error) {
	if root == "" {
		root = defaultRuntimeRoot()
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", "", err
	}
	dir, err = os.MkdirTemp(root, fmt.Sprintf("sess-%d-", os.Getpid()))
	return dir, root, err
}

func exitErr(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return &ExitError{Code: 128 + int(ws.Signal())} // shell convention
		}
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

// chat is the supervisor's view of the running Claude process.
type chat struct {
	o      Options
	sigDir string
	mroot  string
	claude string
	active account.Account
	sid    string // last session id seen from a hook (or --resume)
	pid    int    // the running Claude
	failed map[int]bool
	// Model fallback on the current account: the override in force ("" = the
	// user's own) and the models already capped or tried there.
	model       string
	triedModels map[string]bool
	capped      map[int]time.Time // accounts where this chat ran out of models (chat-local)

	// Wait-for-reset (per Claude process): resume on waitFor at waitUntil
	// unless the user sends a message after waitSince.
	waitUntil time.Time
	waitSince time.Time
	waitFor   account.Account

	started     time.Time // first launch of this supervisor (registry)
	cwd         string
	recordedSid string
	recordedFor int // slot last recorded

	spec       optSpec
	specLoaded bool

	// Per Claude process:
	armedTo  *account.Account // switch target once armed
	armedLim float64          // the line that armed it (targets must be under it)
	idle     time.Time        // when the chat last became idle (clock())
	lastPoll time.Time
	notified bool
	baseline map[int]bool // Claude's child processes when a turn first started (MCP servers)
}

// move is a decided switch; to is provisional until commit prepares it.
type move struct {
	to        account.Account
	limit     float64 // replacement targets must be under this line
	prompt    string  // intercepted user message(s), sent verbatim
	hasPrompt bool
	hard      bool   // caused by a hard limit (no target left ⇒ pool exhausted)
	model     string // same-account model fallback: relaunch with --model model
	fromModel string // …because this model hit its cap
}

// record writes this chat to the session registry (best-effort) when its
// account or Claude session id changed since the last write.
func (c *chat) record() {
	if c.recordedFor == c.active.Slot && c.recordedSid == c.sid {
		return
	}
	pool := make([]int, 0, len(c.o.Pool))
	for _, a := range c.o.Pool {
		pool = append(pool, a.Slot) // configured failover order, as launched
	}
	_ = recordSession(session.Session{Slot: c.active.Slot, PID: os.Getpid(), Cwd: c.cwd, Started: c.started, SessionID: c.sid, Pool: pool})
	c.recordedFor, c.recordedSid = c.active.Slot, c.sid
}

func (c *chat) emit(reason string, a account.Account) {
	emitEvent(c.o.Config.EventCommand, event.Event{SessionID: c.sid, Account: a.Email, Reason: reason, Timestamp: time.Now()})
}

// resetLabel shows a reset time: "15:04" today, else with the date.
func resetLabel(at, now time.Time) string {
	at, now = at.Local(), now.Local()
	if at.Format("2006-01-02") == now.Format("2006-01-02") {
		return at.Format("15:04")
	}
	return at.Format("Mon Jan 2 15:04")
}

// startWait schedules a resume at the pool's earliest reset (spec §5.6, opt-in).
func (c *chat) startWait() {
	a, at, ok := earliestReset(c.o.Pool, usageOf, clock(), c.skipWait, c.isLimited)
	if !ok {
		notify("switcch", "All accounts for this chat are at their limit — no reset time known; not waiting.")
		return
	}
	// A short grace absorbs clock skew between this Mac and the server.
	c.waitFor, c.waitUntil, c.waitSince = a, at.Add(resetGrace), time.Now()
	notify("switcch", fmt.Sprintf("All accounts for this chat are at their limit — continuing on %s at %s.", name(a), resetLabel(at, time.Now())))
}

func (c *chat) skipWait(a account.Account) bool  { return c.failed[a.Slot] }
func (c *chat) isLimited(a account.Account) bool { return limitedRecently(c.mroot, a) }

func (c *chat) skip(a account.Account) bool {
	if t, ok := c.capped[a.Slot]; ok && time.Since(t) < limitedTTL {
		return true
	}
	return c.failed[a.Slot] || limitedRecently(c.mroot, a)
}

func (c *chat) next(limit float64) *account.Account {
	return next(c.o.Pool, c.active, usageOf, limit, c.skip)
}

func (c *chat) optSpec() optSpec {
	if !c.specLoaded {
		c.specLoaded = true
		if txt, err := claudeHelp(c.claude); err == nil {
			if sp := parseHelp(txt); len(sp) > 0 {
				c.spec = sp
			}
		}
	}
	return c.spec
}

func (c *chat) advise(text string) {
	if !c.notified {
		notify("switcch", text)
		c.notified = true
	}
}

func (c *chat) arm(limit float64) {
	if tgt := c.next(limit); tgt != nil {
		c.armedTo, c.armedLim = tgt, limit
		_ = os.WriteFile(filepath.Join(c.sigDir, hooks.ArmedFile), []byte(name(*tgt)), 0o600)
	}
}

func (c *chat) disarm() {
	c.armedTo = nil
	_ = os.Remove(filepath.Join(c.sigDir, hooks.ArmedFile))
}

// retarget re-checks an armed target at switch time (its usage may have moved).
func (c *chat) retarget() *account.Account {
	tgt := c.next(c.armedLim)
	if tgt == nil {
		c.disarm()
	}
	return tgt
}

// backgroundWork reports whether Claude has child processes it didn't have
// when its first turn started (a background Bash task or subagent), which a
// restart would kill.
func (c *chat) backgroundWork() bool {
	if c.baseline == nil {
		return false
	}
	for _, p := range childrenOf(c.pid) {
		if !c.baseline[p] {
			return true
		}
	}
	return false
}

// tick processes pending hook signals and timers; non-nil = move the chat.
func (c *chat) tick() *move {
	cfg := c.o.Config
	auto := cfg.Mode != "advisory"
	now := clock()

	// 1. An intercepted prompt (chat was armed and the user sent a message).
	if s, ok := take(c.sigDir, hooks.Intercept); ok {
		c.sid = cmp.Or(s.SessionID, c.sid)
		lim := cmp.Or(c.armedLim, 100)
		tgt := c.next(lim)
		if decide(hookEvent{kind: hooks.Intercept, hasTarget: tgt != nil, mode: cfg.Mode}) == switchWithPrompt {
			m := &move{to: c.active, limit: lim, prompt: s.Prompt, hasPrompt: true}
			if tgt != nil {
				m.to = *tgt
			}
			return m
		}
	}
	// 2. A hard limit: always switches in auto mode; tells other chats on this account.
	if s, ok := take(c.sigDir, hooks.RateLimited); ok {
		c.sid = cmp.Or(s.SessionID, c.sid)
		// A model-specific cap: try another model on this account first.
		// The account itself isn't exhausted, so no limited marker.
		if s.Model != "" && auto {
			c.triedModels[s.Model] = true
			for _, fm := range cfg.ModelFallback {
				if fm != s.Model && !c.triedModels[fm] {
					c.triedModels[fm] = true
					c.emit(event.LimitHit, c.active)
					return &move{to: c.active, limit: 100, model: fm, fromModel: s.Model}
				}
			}
		}
		if s.Model != "" {
			// Only one model is capped: other chats on this account (maybe on
			// another model) stay put, so no shared marker — but this chat
			// won't come back here for a while (no ping-pong).
			c.capped[c.active.Slot] = time.Now()
		} else {
			markLimited(c.mroot, c.active)
		}
		c.emit(event.LimitHit, c.active)
		tgt := c.next(100)
		if decide(hookEvent{kind: hooks.RateLimited, hasTarget: tgt != nil, mode: cfg.Mode}) == switchAcct {
			return &move{to: *tgt, limit: 100, hard: true}
		}
		if tgt == nil {
			c.emit(event.PoolExhausted, c.active)
			if auto && cfg.WaitForReset {
				c.startWait()
			}
		}
		if !auto {
			c.advise(name(c.active) + " hit its usage limit")
		}
		// No viable account: leave Claude showing its limit (ration).
	}
	busy := exists(filepath.Join(c.sigDir, hooks.BusyFile))
	if busy && c.baseline == nil {
		c.baseline = map[int]bool{}
		for _, p := range childrenOf(c.pid) {
			c.baseline[p] = true
		}
	}
	// 3. Turn end: an armed chat moves now — unless background work would be
	// killed, in which case it stays armed and moves on the next prompt.
	if s, ok := take(c.sigDir, hooks.Stopped); ok {
		c.sid = cmp.Or(s.SessionID, c.sid)
		c.idle = now
		if c.armedTo != nil && auto && !c.backgroundWork() {
			if tgt := c.retarget(); tgt != nil {
				return &move{to: *tgt, limit: c.armedLim}
			}
		}
	}
	if busy {
		c.idle = now
	}
	// 4. Arming: another chat hit a hard limit on this account, or (proactive)
	// this account crossed the threshold.
	if c.armedTo == nil {
		over, lim := false, 100.0
		if limitedRecently(c.mroot, c.active) {
			over = true
		} else if cfg.Trigger == "proactive" && time.Since(c.lastPoll) >= usagePollInterval {
			c.lastPoll = time.Now()
			if u, err := usageOf(c.active); err == nil && peak(u) >= cfg.Threshold {
				over, lim = true, cfg.Threshold
			}
		}
		if over {
			if auto {
				c.arm(lim)
			} else {
				c.advise(fmt.Sprintf("%s is over %.0f%% of its usage — consider switching accounts", name(c.active), lim))
			}
		}
	}
	// Waiting for a reset: the user typing takes over; otherwise resume on time.
	if !c.waitUntil.IsZero() {
		if fi, err := os.Stat(filepath.Join(c.sigDir, hooks.BusyFile)); err == nil && fi.ModTime().After(c.waitSince) {
			c.waitUntil = time.Time{}
		} else if !now.Before(c.waitUntil) {
			to := c.waitFor
			c.waitUntil = time.Time{}
			_ = os.Remove(limitedMarker(c.mroot, to))
			return &move{to: to, limit: 100, prompt: cfg.ResumeMessage, hasPrompt: true}
		}
	}
	// 5. Optional: move an armed chat that has sat idle long enough.
	if c.armedTo != nil && auto && !busy && c.sid != "" && cfg.IdleMigrateMinutes > 0 &&
		now.Sub(c.idle) >= time.Duration(cfg.IdleMigrateMinutes)*time.Minute && !c.backgroundWork() {
		if tgt := c.retarget(); tgt != nil {
			return &move{to: *tgt, limit: c.armedLim}
		}
	}
	return nil
}

// commit prepares the move's target BEFORE the running Claude is stopped. A
// target that can't be prepared (e.g. its login broke) is skipped for the rest
// of the session and the next one is tried; with none left, a carried prompt
// is delivered on the current account and anything else stays put.
func (c *chat) commit(m *move) ([]string, bool) {
	for {
		if m.to.Slot == c.active.Slot && !m.hasPrompt && m.model == "" {
			return nil, false
		}
		_, env, err := prepare(m.to)
		if err == nil {
			return env, true
		}
		if m.to.Slot == c.active.Slot {
			return nil, false
		}
		notify("switcch", fmt.Sprintf("Can't switch to %s (%v) — skipping it this session", name(m.to), err))
		c.failed[m.to.Slot] = true
		if tgt := c.next(m.limit); tgt != nil {
			m.to = *tgt
			continue
		}
		if !m.hasPrompt {
			c.disarm()
			if m.hard {
				c.emit(event.PoolExhausted, c.active)
			}
			return nil, false
		}
		m.to = c.active
	}
}

// message is the relaunch's first message: the notice (when changing account)
// and any carried prompt, verbatim.
func (c *chat) message(m *move) string {
	var msg string
	switch {
	case m.model != "":
		msg = strings.NewReplacer("{account}", name(c.active), "{from}", m.fromModel, "{to}", m.model).Replace(c.o.Config.ModelNotice)
	case m.to.Slot == c.active.Slot:
		return m.prompt
	default:
		msg = notice(c.o.Config.Notice, c.active, m.to)
	}
	if m.hasPrompt {
		msg += "\n\n" + m.prompt
	}
	return msg
}

// Run supervises one chat until the user ends it.
func Run(ctx context.Context, o Options) error {
	if len(o.Pool) == 0 {
		return errors.New("supervise: empty account pool")
	}
	sigDir, root, err := signalDir(o.RuntimeDir)
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

	c := &chat{o: o, sigDir: sigDir, mroot: markerRoot(root), claude: claude, active: o.Pool[0],
		sid: resumeID(o.ClaudeArgs), failed: map[int]bool{}, triedModels: map[string]bool{}, capped: map[int]time.Time{}, started: time.Now()}
	c.cwd, _ = os.Getwd()
	_, env, err := prepare(c.active)
	if err != nil {
		return err
	}
	args := o.ClaudeArgs
	var carried string // user message(s) handed to the current Claude via argv
	var movedAt time.Time
	for {
		clearMarkers(sigDir)
		c.armedTo, c.armedLim, c.notified, c.lastPoll, c.idle, c.baseline = nil, 0, false, time.Time{}, clock(), nil
		c.waitUntil = time.Time{}
		cmd := command(claude, args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		cmd.Env = childEnv(env, sigDir)
		if err := cmd.Start(); err != nil {
			return err
		}
		c.pid = cmd.Process.Pid
		_ = os.WriteFile(filepath.Join(sigDir, hooks.ChildPIDFile), []byte(strconv.Itoa(c.pid)), 0o600)
		c.record()
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()

		var m *move
		var nextEnv []string
		for m == nil {
			select {
			case err := <-exited:
				captureBack(c.active)
				e := exitErr(err)
				if e != nil && carried != "" && time.Since(movedAt) < earlyExit {
					fmt.Fprintf(stderr, "\nswitcch: Claude exited right after this chat moved to %s, so your message may not have been sent. Here it is:\n\n%s\n\n", name(c.active), carried)
				}
				return e
			case s := <-term:
				_ = cmd.Process.Signal(s)
				<-exited
				captureBack(c.active)
				if sig, ok := s.(syscall.Signal); ok {
					return &ExitError{Code: 128 + int(sig)}
				}
				return nil
			case <-ctx.Done():
				stopChild(cmd, exited)
				captureBack(c.active)
				return ctx.Err()
			case <-time.After(pollInterval):
				mv := c.tick()
				c.record()
				if mv == nil {
					continue
				}
				if e, ok := c.commit(mv); ok {
					m, nextEnv = mv, e
				} else if mv.hasPrompt {
					notify("switcch", "Couldn't deliver your message — please send it again: "+mv.prompt)
				}
			}
		}

		// Arm before stopping, so a message typed while Claude shuts down is
		// blocked by the hook and handed over rather than lost.
		_ = os.WriteFile(filepath.Join(sigDir, hooks.ArmedFile), []byte(name(m.to)), 0o600)
		if m.to.Slot != c.active.Slot {
			fmt.Fprintf(stderr, "\nswitcch: moving this chat %s → %s…\n", name(c.active), name(m.to))
		}
		stopChild(cmd, exited)
		if s, ok := take(sigDir, hooks.Intercept); ok {
			if m.hasPrompt {
				m.prompt += "\n\n" + s.Prompt
			} else {
				m.prompt, m.hasPrompt = s.Prompt, true
			}
		}
		captureBack(c.active)
		carried, movedAt = "", time.Now()
		if m.hasPrompt {
			carried = m.prompt
		}
		msg := c.message(m)
		if m.to.Slot != c.active.Slot { // new account: its own model allowances
			c.model, c.triedModels = "", map[string]bool{}
		}
		if m.model != "" {
			c.model = m.model
		}
		args = relaunchArgs(o.ClaudeArgs, c.optSpec(), c.model, c.sid, msg)
		c.active, env = m.to, nextEnv
	}
}

// earliestReset returns the pool account that frees up first, and when.
// An account is free when every window at/over 100% has reset (its time is the
// latest such reset). An account that recently hit a hard limit (limited) but
// whose cached reading is still under 100% is bound by its fullest window.
// Resets at or before now (stale readings) and skipped accounts don't count;
// an account at 100% with an unknown reset time is ignored.
func earliestReset(pool []account.Account, usageOf func(account.Account) (usage.Usage, error), now time.Time, skip, limited func(account.Account) bool) (account.Account, time.Time, bool) {
	var best account.Account
	var bestAt time.Time
	for _, a := range pool {
		if skip(a) {
			continue
		}
		u, err := usageOf(a)
		if err != nil {
			continue
		}
		windows := []usage.Window{u.FiveHour, u.SevenDay}
		var at time.Time
		known, full := true, false
		for _, w := range windows {
			if w.Utilization < 100 {
				continue
			}
			full = true
			if !w.HasReset {
				known = false
				break
			}
			if w.ResetsAt.After(at) {
				at = w.ResetsAt
			}
		}
		if !known {
			continue
		}
		if !full && limited(a) {
			var top *usage.Window
			for i := range windows {
				w := &windows[i]
				if w.HasReset && w.ResetsAt.After(now) && (top == nil || w.Utilization > top.Utilization) {
					top = w
				}
			}
			if top != nil {
				at = top.ResetsAt
			}
		}
		if !at.After(now) {
			continue // nothing to wait for, or a stale reset already past
		}
		if bestAt.IsZero() || at.Before(bestAt) {
			best, bestAt = a, at
		}
	}
	return best, bestAt, !bestAt.IsZero()
}
