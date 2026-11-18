//go:build darwin

package supervise

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/00mkp/switcch/internal/account"
	"github.com/00mkp/switcch/internal/config"
	"github.com/00mkp/switcch/internal/event"
	"github.com/00mkp/switcch/internal/hooks"
	"github.com/00mkp/switcch/internal/session"
	"github.com/00mkp/switcch/internal/usage"
)

var (
	work     = account.Account{Slot: 1, Email: "w@x.com", Alias: "work"}
	personal = account.Account{Slot: 2, Email: "p@x.com", Alias: "personal"}
	other    = account.Account{Slot: 3, Email: "o@x.com", Alias: "other"}
)

func usageMap(m map[int]float64) func(account.Account) (usage.Usage, error) {
	return func(a account.Account) (usage.Usage, error) {
		return usage.Usage{FiveHour: usage.Window{Utilization: m[a.Slot]}}, nil
	}
}

func TestNextSkipsExhaustedAndPins(t *testing.T) {
	pool := []account.Account{work, personal, other}
	none := func(account.Account) bool { return false }
	got := next(pool, work, usageMap(map[int]float64{2: 100, 3: 10}), 100, none)
	if got == nil || got.Slot != other.Slot {
		t.Fatalf("next = %+v, want other (personal is exhausted)", got)
	}
	if got := next([]account.Account{personal}, personal, usageMap(nil), 100, none); got != nil {
		t.Fatalf("a pin has no next account, got %+v", got)
	}
	if got := next(pool, work, usageMap(map[int]float64{2: 95, 3: 95}), 90, none); got != nil {
		t.Fatalf("proactive limit must exclude accounts already over it, got %+v", got)
	}
	skipPersonal := func(a account.Account) bool { return a.Slot == personal.Slot }
	if got := next(pool, work, usageMap(nil), 100, skipPersonal); got == nil || got.Slot != other.Slot {
		t.Fatalf("a skipped (recently limited / unusable) account must not be chosen, got %+v", got)
	}
}

func TestDecide(t *testing.T) {
	cases := []struct {
		ev   hookEvent
		want action
	}{
		{hookEvent{kind: hooks.RateLimited, hasTarget: true, mode: "auto"}, switchAcct},
		{hookEvent{kind: hooks.RateLimited, hasTarget: false, mode: "auto"}, stay},
		{hookEvent{kind: hooks.RateLimited, hasTarget: true, mode: "advisory"}, stay},
		{hookEvent{kind: hooks.RateLimited, hasTarget: true, mode: ""}, switchAcct}, // one predicate for "auto"
		{hookEvent{kind: hooks.Intercept, hasTarget: true, mode: "auto"}, switchWithPrompt},
		{hookEvent{exited: true}, done},
	}
	for _, c := range cases {
		if got := decide(c.ev); got != c.want {
			t.Errorf("decide(%+v) = %v, want %v", c.ev, got, c.want)
		}
	}
}

const sampleHelp = `Usage: claude [options] [command] [prompt]

Options:
  --add-dir <directories...>            More directories Claude can access
  --model <model>                       Model for the current session
  -c, --continue                        Continue the most recent conversation
  -d, --debug [filter]                  Enable debug mode
  -r, --resume [value]                  Resume a conversation
  --session-id <uuid>                   Use a specific session ID
  --fork-session                        When resuming, create a new session ID
  --allowedTools, --allowed-tools <tools...>
  --verbose                             Override verbose mode
`

func TestParseHelp(t *testing.T) {
	sp := parseHelp(sampleHelp)
	want := map[string]arity{"--add-dir": variadic, "--model": required, "-c": none, "--continue": none,
		"-d": optional, "-r": optional, "--resume": optional, "--allowed-tools": variadic, "--allowedTools": variadic, "--verbose": none}
	for k, v := range want {
		if sp[k] != v {
			t.Errorf("%s arity = %v, want %v", k, sp[k], v)
		}
	}
}

func TestRelaunchArgs(t *testing.T) {
	sp := parseHelp(sampleHelp)
	got := relaunchArgs([]string{"--model", "opus", "commit and push", "--resume", "old-id", "--add-dir", "a", "b", "-c", "--verbose"}, sp, "sid-9", "- hello")
	want := []string{"--model", "opus", "--add-dir", "a", "b", "--verbose", "--resume", "sid-9", "--", "- hello"}
	if !slices.Equal(got, want) {
		t.Fatalf("relaunchArgs =\n %q\nwant\n %q", got, want)
	}
	// Everything after the user's own "--" is positional: dropped.
	if got := relaunchArgs([]string{"--verbose", "--", "--model"}, sp, "s", "m"); !slices.Equal(got, []string{"--verbose", "--resume", "s", "--", "m"}) {
		t.Fatalf("after --: %q", got)
	}
	if got := relaunchArgs([]string{"--model=opus", "hi"}, sp, "s", "m"); !slices.Equal(got, []string{"--model=opus", "--resume", "s", "--", "m"}) {
		t.Fatalf("--flag=value: %q", got)
	}
	if got := relaunchArgs(nil, sp, "", "hi"); !slices.Equal(got, []string{"--continue", "--", "hi"}) {
		t.Fatalf("no session id should fall back to --continue, got %q", got)
	}
	// Unknown option table (claude --help failed): keep nothing but the resume.
	if got := relaunchArgs([]string{"--model", "opus", "do X"}, nil, "s", "m"); !slices.Equal(got, []string{"--resume", "s", "--", "m"}) {
		t.Fatalf("without a flag table nothing of the user's args may be guessed at: %q", got)
	}
}

// resumeOf parses "... --resume <sid> -- <msg>" (the msg must be last).
func resumeOf(a []string) (sid, msg string, ok bool) {
	i := slices.Index(a, "--resume")
	if i < 0 || i+3 != len(a)-1 || a[i+2] != "--" {
		return "", "", false
	}
	return a[i+1], a[i+3], true
}

// ---- fake claude ----

type launchRec struct {
	Args      []string `json:"args"`
	ConfigDir string   `json:"config_dir"`
	Watchdog  string   `json:"watchdog"`
	PIDFileOK bool     `json:"pid_file_ok"`
}

// TestHelperProcess is the fake ` + "`claude`" + `. FAKE_ACT applies to launch 1
// (or every launch with an "-always" suffix): emit a hook signal, then block
// until SIGTERM or FAKE_WAIT. Other launches exit at once (FAKE_EXIT_LATER code).
func TestHelperProcess(t *testing.T) {
	if os.Getenv("SWITCCH_FAKE_CLAUDE") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	dir := os.Getenv("SWITCCH_SIGNAL_DIR")
	pidOK := false
	for i := 0; i < 40 && !pidOK; i++ { // the supervisor writes it right after Start
		b, _ := os.ReadFile(filepath.Join(dir, hooks.ChildPIDFile))
		pidOK = strings.TrimSpace(string(b)) == strconv.Itoa(os.Getpid())
		if !pidOK {
			time.Sleep(5 * time.Millisecond)
		}
	}
	logp := os.Getenv("FAKE_LOG")
	rec, _ := json.Marshal(launchRec{Args: args, ConfigDir: os.Getenv("CLAUDE_CONFIG_DIR"), Watchdog: os.Getenv("CLAUDE_CODE_RETRY_WATCHDOG"), PIDFileOK: pidOK})
	f, _ := os.OpenFile(logp, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	f.Write(append(rec, '\n'))
	f.Close()
	n := countLines(logp)
	act, always := strings.CutSuffix(os.Getenv("FAKE_ACT"), "-always")
	if (n == 1 || always) && act != "none" {
		term := make(chan os.Signal, 1)
		signal.Notify(term, syscall.SIGTERM)
		switch act {
		case "ratelimit":
			hooks.WriteSignal(dir, hooks.Signal{Kind: hooks.RateLimited, SessionID: "sid-1"})
		case "intercept":
			hooks.WriteSignal(dir, hooks.Signal{Kind: hooks.Intercept, SessionID: "sid-1", Prompt: os.Getenv("FAKE_PROMPT")})
		}
		wait := 10 * time.Second
		if d, err := time.ParseDuration(os.Getenv("FAKE_WAIT")); err == nil {
			wait = d
		}
		select {
		case <-term:
			// The user typed while we were being stopped: the (armed) hook
			// would block it and hand it over.
			if p := os.Getenv("FAKE_TERM_PROMPT"); p != "" {
				if _, err := os.Stat(filepath.Join(dir, hooks.ArmedFile)); err == nil {
					hooks.WriteSignal(dir, hooks.Signal{Kind: hooks.Intercept, SessionID: "sid-1", Prompt: p})
				}
			}
		case <-time.After(wait):
		}
		os.Exit(0)
	}
	if c, err := strconv.Atoi(os.Getenv("FAKE_EXIT_LATER")); err == nil {
		os.Exit(c)
	}
	os.Exit(0)
}

func countLines(p string) int {
	f, err := os.Open(p)
	if err != nil {
		return 0
	}
	defer f.Close()
	n := 0
	s := bufio.NewScanner(f)
	for s.Scan() {
		n++
	}
	return n
}

func readLaunches(t *testing.T, p string) []launchRec {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var out []launchRec
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var r launchRec
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

type fake struct {
	act, prompt, termPrompt string
	exitLater               int
	pool                    []account.Account
	prepareFail             map[string]bool
	claudeArgs              []string
	mode                    string
}

// fakeSeams points the supervisor at the fake claude; returns the log path.
func fakeSeams(t *testing.T, f fake, extraEnv ...string) string {
	t.Helper()
	logp := filepath.Join(t.TempDir(), "launches.jsonl")
	o1, o2, o3, o4, o5, o6, o7, o8, o9 := prepare, usageOf, captureBack, command, claudePath, pollInterval, claudeHelp, stderr, childrenOf
	t.Cleanup(func() {
		prepare, usageOf, captureBack, command, claudePath, pollInterval, claudeHelp, stderr, childrenOf = o1, o2, o3, o4, o5, o6, o7, o8, o9
	})
	prepare = func(a account.Account) (string, []string, error) {
		if f.prepareFail[a.Alias] {
			return "", nil, errors.New("invalid_grant")
		}
		dir := "/profiles/" + a.Alias
		return dir, append([]string{
			"CLAUDE_CONFIG_DIR=" + dir, "CLAUDE_CODE_RETRY_WATCHDOG=1",
			"SWITCCH_FAKE_CLAUDE=1", "FAKE_LOG=" + logp, "FAKE_ACT=" + f.act, "FAKE_PROMPT=" + f.prompt,
			"FAKE_TERM_PROMPT=" + f.termPrompt, "FAKE_EXIT_LATER=" + strconv.Itoa(f.exitLater),
		}, extraEnv...), nil
	}
	usageOf = usageMap(nil)
	captureBack = func(account.Account) {}
	claudePath = func() (string, error) { return os.Args[0], nil }
	claudeHelp = func(string) (string, error) { return sampleHelp, nil }
	childrenOf = func(int) []int { return nil }
	command = func(name string, args ...string) *exec.Cmd {
		return exec.Command(name, append([]string{"-test.run=TestHelperProcess", "--"}, args...)...)
	}
	pollInterval = 10 * time.Millisecond
	return logp
}

func runFakeErr(t *testing.T, f fake) ([]launchRec, string, error) {
	t.Helper()
	logp := fakeSeams(t, f, "FAKE_WAIT=300ms")
	var errBuf strings.Builder
	stderr = &errBuf
	on := notify
	t.Cleanup(func() { notify = on })
	notify = func(_, msg string) { errBuf.WriteString("[notify] " + msg + "\n") }
	cfg := config.Defaults()
	if f.mode != "" {
		cfg.Mode = f.mode
	}
	// A deadline turns a relaunch loop into a failure instead of a hung test.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := Run(ctx, Options{Pool: f.pool, Config: cfg, ClaudeArgs: f.claudeArgs, RuntimeDir: t.TempDir()})
	return readLaunches(t, logp), errBuf.String(), err
}

func runFake(t *testing.T, act, prompt string, pool []account.Account) []launchRec {
	t.Helper()
	l, _, err := runFakeErr(t, fake{act: act, prompt: prompt, pool: pool})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return l
}

func TestRunRateLimitFailsOverToSameConversation(t *testing.T) {
	l := runFake(t, "ratelimit", "", []account.Account{work, personal})
	if len(l) != 2 {
		t.Fatalf("want 2 launches, got %d: %+v", len(l), l)
	}
	if l[1].ConfigDir != "/profiles/personal" {
		t.Fatalf("relaunch should be on personal's profile, got %q", l[1].ConfigDir)
	}
	sid, msg, ok := resumeOf(l[1].Args)
	if !ok || sid != "sid-1" {
		t.Fatalf("relaunch must resume the same session, got %v", l[1].Args)
	}
	if !strings.Contains(msg, "work") || !strings.Contains(msg, "personal") {
		t.Fatalf("notice should name both accounts, got %q", msg)
	}
	if l[0].Watchdog != "" || l[1].Watchdog != "" {
		t.Fatal("CLAUDE_CODE_RETRY_WATCHDOG must be unset in the child (it can suppress StopFailure)")
	}
	if !l[0].PIDFileOK || !l[1].PIDFileOK {
		t.Fatal("the supervisor must record each child's pid for the hook's ancestry check")
	}
}

func TestRunInterceptCarriesPromptVerbatim(t *testing.T) {
	prompt := "- line one\nwith \"quotes\" and unicode ✓"
	l := runFake(t, "intercept", prompt, []account.Account{work, personal})
	if len(l) != 2 {
		t.Fatalf("want 2 launches, got %d", len(l))
	}
	_, msg, ok := resumeOf(l[1].Args)
	if !ok || !strings.HasSuffix(msg, "\n\n"+prompt) {
		t.Fatalf("intercepted prompt must arrive verbatim after the notice (and after --), got %q", l[1].Args)
	}
	if l[1].ConfigDir != "/profiles/personal" {
		t.Fatalf("intercept should move to personal, got %q", l[1].ConfigDir)
	}
}

func TestRunDoesNotReplayLaunchPrompt(t *testing.T) {
	l, _, err := runFakeErr(t, fake{act: "ratelimit", pool: []account.Account{work, personal},
		claudeArgs: []string{"--model", "opus", "commit and push the fix"}})
	if err != nil || len(l) != 2 {
		t.Fatalf("err=%v launches=%d", err, len(l))
	}
	if slices.Contains(l[1].Args, "commit and push the fix") {
		t.Fatalf("the launch prompt must not be re-sent on relaunch: %q", l[1].Args)
	}
	if !slices.Contains(l[1].Args, "opus") {
		t.Fatalf("the user's flags must be kept: %q", l[1].Args)
	}
}

func TestRunUserExitEndsWithNoRelaunch(t *testing.T) {
	l := runFake(t, "none", "", []account.Account{work, personal})
	if len(l) != 1 {
		t.Fatalf("a normal exit must end switcch with no relaunch, got %d launches", len(l))
	}
}

func TestRunPinnedRateLimitDoesNotLoop(t *testing.T) {
	l := runFake(t, "ratelimit", "", []account.Account{personal})
	if len(l) != 1 {
		t.Fatalf("a pinned chat must not be relaunched on a limit, got %d launches", len(l))
	}
}

func TestRunHardLimitsEverywhereDoNotPingPong(t *testing.T) {
	// Both accounts reject every turn (e.g. a model cap the 5h/7d numbers don't
	// show): one move, then stay — never bounce back to the account that just failed.
	l, _, err := runFakeErr(t, fake{act: "ratelimit-always", pool: []account.Account{work, personal}})
	if err != nil {
		t.Fatal(err)
	}
	if len(l) != 2 {
		t.Fatalf("want exactly one move then stay, got %d launches", len(l))
	}
}

func TestRunTargetThatCannotPrepareIsSkipped(t *testing.T) {
	// personal's login is broken: a hard limit on work must keep work's Claude
	// running (not kill it first) and fall through to the next pool account.
	l, errOut, err := runFakeErr(t, fake{act: "ratelimit", pool: []account.Account{work, personal, other},
		prepareFail: map[string]bool{"personal": true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(l) != 2 || l[1].ConfigDir != "/profiles/other" {
		t.Fatalf("want a move to other, got %+v", l)
	}
	if !strings.Contains(errOut, "personal") {
		t.Fatalf("the skipped account should be reported, stderr=%q", errOut)
	}
	// No viable target at all: stay on work, Claude untouched.
	l, _, err = runFakeErr(t, fake{act: "ratelimit", pool: []account.Account{work, personal},
		prepareFail: map[string]bool{"personal": true}})
	if err != nil || len(l) != 1 {
		t.Fatalf("no preparable target ⇒ stay: err=%v launches=%d", err, len(l))
	}
}

func TestRunInterceptWithUnpreparableTargetDeliversHere(t *testing.T) {
	l, _, err := runFakeErr(t, fake{act: "intercept", prompt: "hello", pool: []account.Account{work, personal},
		prepareFail: map[string]bool{"personal": true}})
	if err != nil || len(l) != 2 || l[1].ConfigDir != "/profiles/work" {
		t.Fatalf("the blocked prompt must be delivered on the current account: err=%v %+v", err, l)
	}
	if _, msg, ok := resumeOf(l[1].Args); !ok || msg != "hello" {
		t.Fatalf("message = %q (no notice when not moving)", l[1].Args)
	}
}

func TestRunEarlyRelaunchFailurePrintsCarriedPrompt(t *testing.T) {
	_, errOut, err := runFakeErr(t, fake{act: "intercept", prompt: "my precious message", exitLater: 1,
		pool: []account.Account{work, personal}})
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 1 {
		t.Fatalf("want claude's exit status, got %v", err)
	}
	if !strings.Contains(errOut, "my precious message") {
		t.Fatalf("a prompt carried into a relaunch that died must be shown to the user, stderr=%q", errOut)
	}
}

func TestRunPromptTypedWhileStoppingIsCarried(t *testing.T) {
	// Hard-limit move: the user types while Claude is being stopped. The
	// supervisor must have armed the hook (so it blocks + hands over the prompt)
	// and the relaunch must carry it.
	l, _, err := runFakeErr(t, fake{act: "ratelimit", termPrompt: "typed during stop", pool: []account.Account{work, personal}})
	if err != nil || len(l) != 2 {
		t.Fatalf("err=%v launches=%d", err, len(l))
	}
	if _, msg, _ := resumeOf(l[1].Args); !strings.HasSuffix(msg, "\n\ntyped during stop") {
		t.Fatalf("prompt typed during the stop was lost: %q", l[1].Args)
	}
	// Intercept move plus a second prompt during the stop: both arrive, in order.
	l, _, err = runFakeErr(t, fake{act: "intercept", prompt: "first", termPrompt: "second", pool: []account.Account{work, personal}})
	if err != nil || len(l) != 2 {
		t.Fatalf("err=%v launches=%d", err, len(l))
	}
	if _, msg, _ := resumeOf(l[1].Args); !strings.HasSuffix(msg, "first\n\nsecond") {
		t.Fatalf("both prompts must be carried in order: %q", l[1].Args)
	}
}

// ---- Task 6: proactive threshold, advisory, idle migration ----

// fakeChild is a scripted Claude run: the test drives signals into its dir.
type step struct {
	after time.Duration
	do    func(dir string)
}

func scripted(t *testing.T, cfg config.Config, pool []account.Account, util map[int]float64, steps []step) (launches []launchRec, polls int, notes []string, root string) {
	t.Helper()
	root = t.TempDir()
	logp := fakeSeams(t, fake{act: "block"}, "FAKE_WAIT=700ms")
	o1, o2, o3 := usagePollInterval, notify, clock
	t.Cleanup(func() { usagePollInterval, notify, clock = o1, o2, o3 })
	if scriptedChildren != nil {
		childrenOf = scriptedChildren
	}
	usageOf = func(a account.Account) (usage.Usage, error) {
		if a.Slot == pool[0].Slot {
			polls++
		}
		return usage.Usage{FiveHour: usage.Window{Utilization: util[a.Slot]}}, nil
	}
	pollInterval = 5 * time.Millisecond
	usagePollInterval = 20 * time.Millisecond
	notify = func(title, msg string) { notes = append(notes, msg) }
	// A fake clock that runs 1000x fast so idle minutes elapse in milliseconds.
	start := time.Now()
	clock = func() time.Time { return start.Add(time.Since(start) * 1000) }

	go func() {
		// Time steps from when the first Claude is actually running (its pid
		// is recorded), not from Run's start: under load, starting the fake
		// can take long enough that it would exit before a step fires.
		for i := 0; i < 500; i++ {
			if m, _ := filepath.Glob(filepath.Join(root, "sess-*", hooks.ChildPIDFile)); len(m) > 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		for _, s := range steps {
			time.Sleep(s.after)
			dirs, _ := filepath.Glob(filepath.Join(root, "sess-*"))
			if len(dirs) == 1 {
				s.do(dirs[0])
			}
		}
	}()
	if err := Run(context.Background(), Options{Pool: pool, Config: cfg, ClaudeArgs: scriptedArgs, RuntimeDir: root}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return readLaunches(t, logp), polls, notes, root
}

// scriptedArgs / scriptedChildren customise the next scripted run.
var (
	scriptedArgs     []string
	scriptedChildren func(int) []int
)

func busy(dir string) { _ = os.WriteFile(filepath.Join(dir, hooks.BusyFile), nil, 0o600) }
func stopped(dir string) {
	_ = os.Remove(filepath.Join(dir, hooks.BusyFile))
	_ = hooks.WriteSignal(dir, hooks.Signal{Kind: hooks.Stopped, SessionID: "sid-1"})
}

func proactive() config.Config { c := config.Defaults(); return c } // proactive, 90, auto

func TestProactiveSwitchesAtTurnEnd(t *testing.T) {
	l, _, _, _ := scripted(t, proactive(), []account.Account{work, personal}, map[int]float64{1: 91},
		[]step{{10 * time.Millisecond, busy}, {150 * time.Millisecond, stopped}})
	if len(l) != 2 || l[1].ConfigDir != "/profiles/personal" {
		t.Fatalf("proactive+auto must switch at turn end, got %+v", l)
	}
	if sid, _, ok := resumeOf(l[1].Args); !ok || sid != "sid-1" {
		t.Fatalf("must resume the same chat, got %v", l[1].Args)
	}
}

func TestProactiveDoesNotCutOffARunningTurn(t *testing.T) {
	l, _, _, _ := scripted(t, proactive(), []account.Account{work, personal}, map[int]float64{1: 91},
		[]step{{10 * time.Millisecond, busy}})
	if len(l) != 1 {
		t.Fatalf("no Stopped yet ⇒ no switch, got %d launches", len(l))
	}
}

func TestProactiveIdleArmsWithoutRestart(t *testing.T) {
	var armed string
	l, _, _, _ := scripted(t, proactive(), []account.Account{work, personal}, map[int]float64{1: 91},
		[]step{{200 * time.Millisecond, func(d string) {
			b, _ := os.ReadFile(filepath.Join(d, hooks.ArmedFile))
			armed = string(b)
		}}})
	if len(l) != 1 {
		t.Fatalf("an idle chat must not be restarted (idleMigrateMinutes=0), got %d launches", len(l))
	}
	if armed != "personal" {
		t.Fatalf("armed marker = %q, want the next account's name", armed)
	}
}

func TestIdleMigrateAfterMinutes(t *testing.T) {
	c := proactive()
	c.IdleMigrateMinutes = 5
	scriptedArgs = []string{"--resume", "sid-1"} // session known without any hook firing
	t.Cleanup(func() { scriptedArgs = nil })
	// 1000x clock: 5 idle minutes ≈ 300ms real; the fake Claude lives 700ms.
	l, _, _, _ := scripted(t, c, []account.Account{work, personal}, map[int]float64{1: 50}, nil)
	if len(l) != 1 {
		t.Fatalf("below threshold an idle chat must stay, got %d", len(l))
	}
	l, _, _, _ = scripted(t, c, []account.Account{work, personal}, map[int]float64{1: 91}, nil)
	if len(l) != 2 || l[1].ConfigDir != "/profiles/personal" {
		t.Fatalf("armed + idle past idleMigrateMinutes must switch, got %+v", l)
	}
	if sid, _, ok := resumeOf(l[1].Args); !ok || sid != "sid-1" {
		t.Fatalf("idle migration must resume the same chat, got %v", l[1].Args)
	}
	c.IdleMigrateMinutes = 0
	l, _, _, _ = scripted(t, c, []account.Account{work, personal}, map[int]float64{1: 91}, nil)
	if len(l) != 1 {
		t.Fatalf("idleMigrateMinutes=0 must never move an idle chat, got %d", len(l))
	}
}

func TestAdvisoryNotifiesOnceNeverSwitches(t *testing.T) {
	c := proactive()
	c.Mode = "advisory"
	l, _, notes, _ := scripted(t, c, []account.Account{work, personal}, map[int]float64{1: 91},
		[]step{{10 * time.Millisecond, busy}, {100 * time.Millisecond, stopped}})
	if len(l) != 1 {
		t.Fatalf("advisory must never switch, got %d launches", len(l))
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "work") {
		t.Fatalf("advisory must notify exactly once, got %q", notes)
	}
}

func TestReactiveNeverPollsUsage(t *testing.T) {
	c := proactive()
	c.Trigger = "reactive"
	l, polls, _, _ := scripted(t, c, []account.Account{work, personal}, map[int]float64{1: 91},
		[]step{{10 * time.Millisecond, busy}, {100 * time.Millisecond, stopped}})
	if polls != 0 || len(l) != 1 {
		t.Fatalf("reactive: polls=%d launches=%d, want 0 and 1", polls, len(l))
	}
}

func TestHardLimitMarksAccountForOtherChats(t *testing.T) {
	// Chat A hits a hard limit on work; it leaves a limited marker for work so
	// chat B (another supervisor on work) arms without waiting for its own poll.
	c := proactive()
	c.Trigger = "reactive"
	_, _, _, root := scripted(t, c, []account.Account{work, personal}, nil,
		[]step{{10 * time.Millisecond, func(d string) {
			_ = hooks.WriteSignal(d, hooks.Signal{Kind: hooks.RateLimited, SessionID: "sid-1"})
		}}})
	if _, err := os.Stat(limitedMarker(root, work)); err != nil {
		t.Fatalf("hard limit must leave a limited marker for work: %v", err)
	}
	// Chat B, also reactive (so only the marker can arm it), idle on work.
	var armed string
	o := markerRoot
	t.Cleanup(func() { markerRoot = o })
	markerRoot = func(string) string { return root }
	l, _, _, _ := scripted(t, c, []account.Account{work, personal}, nil,
		[]step{{150 * time.Millisecond, func(d string) {
			b, _ := os.ReadFile(filepath.Join(d, hooks.ArmedFile))
			armed = string(b)
		}}})
	if len(l) != 1 || armed != "personal" {
		t.Fatalf("other chat on work must arm (not restart): launches=%d armed=%q", len(l), armed)
	}
}

func TestPromptSubmittedDuringSwitchIsNotLost(t *testing.T) {
	// Turn ends (proactive switch decided) and the user types before Claude is
	// stopped: the hook intercepts; the relaunch must carry that prompt too.
	l, _, _, _ := scripted(t, proactive(), []account.Account{work, personal}, map[int]float64{1: 91},
		[]step{{10 * time.Millisecond, busy}, {100 * time.Millisecond, func(d string) {
			stopped(d)
			_ = hooks.WriteSignal(d, hooks.Signal{Kind: hooks.Intercept, SessionID: "sid-1", Prompt: "late msg"})
		}}})
	if len(l) != 2 {
		t.Fatalf("want 2 launches, got %d", len(l))
	}
	if _, msg, ok := resumeOf(l[1].Args); !ok || !strings.HasSuffix(msg, "\n\nlate msg") {
		t.Fatalf("late prompt must be delivered on the new account, got %+v", l)
	}
}

func TestTurnEndSwitchWaitsForBackgroundWork(t *testing.T) {
	// A background task the turn started (a new child process of Claude) is
	// still running when the turn ends: don't kill it — stay armed, so the next
	// prompt moves the chat instead.
	var mu sync.Mutex
	kids := []int{11} // an MCP server, present when the turn started
	scriptedChildren = func(int) []int { mu.Lock(); defer mu.Unlock(); return slices.Clone(kids) }
	t.Cleanup(func() { scriptedChildren = nil })
	var armed string
	l, _, _, _ := scripted(t, proactive(), []account.Account{work, personal}, map[int]float64{1: 91},
		[]step{{10 * time.Millisecond, busy},
			{50 * time.Millisecond, func(string) { mu.Lock(); kids = append(kids, 22); mu.Unlock() }},
			{50 * time.Millisecond, stopped},
			{100 * time.Millisecond, func(d string) { b, _ := os.ReadFile(filepath.Join(d, hooks.ArmedFile)); armed = string(b) }}})
	if len(l) != 1 {
		t.Fatalf("turn-end switch must wait while background work runs, got %d launches", len(l))
	}
	if armed != "personal" {
		t.Fatalf("the chat must stay armed for its next prompt, armed=%q", armed)
	}
}

func TestExitErrSignal(t *testing.T) {
	err := exec.Command("sh", "-c", "kill -TERM $$").Run()
	var ee *ExitError
	if !errors.As(exitErr(err), &ee) || ee.Code != 143 {
		t.Fatalf("a child killed by SIGTERM must exit 143, got %v", exitErr(err))
	}
	if exitErr(exec.Command("true").Run()) != nil {
		t.Fatal("clean exit must be nil")
	}
}

func TestSupervisorRecordsSession(t *testing.T) {
	var mu sync.Mutex
	var recs []session.Session
	o := recordSession
	t.Cleanup(func() { recordSession = o })
	recordSession = func(s session.Session) error { mu.Lock(); recs = append(recs, s); mu.Unlock(); return nil }
	if _, _, err := runFakeErr(t, fake{act: "ratelimit", pool: []account.Account{work, personal}}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(recs) < 2 || recs[0].Slot != 1 || recs[len(recs)-1].Slot != 2 {
		t.Fatalf("records must follow the chat from work to personal: %+v", recs)
	}
	last := recs[len(recs)-1]
	if last.PID != os.Getpid() || last.SessionID != "sid-1" || !slices.Equal(last.Pool, []int{1, 2}) || !last.Started.Equal(recs[0].Started) {
		t.Fatalf("record = %+v (first %+v)", last, recs[0])
	}
}

func TestRunEmitsLimitAndExhaustion(t *testing.T) {
	var mu sync.Mutex
	var evs []event.Event
	o := emitEvent
	t.Cleanup(func() { emitEvent = o })
	emitEvent = func(cmd string, e event.Event) { mu.Lock(); evs = append(evs, e); mu.Unlock() }
	if _, _, err := runFakeErr(t, fake{act: "ratelimit-always", pool: []account.Account{work, personal}}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	var got []string
	for _, e := range evs {
		got = append(got, e.Reason+"/"+e.Account)
		if e.SessionID != "sid-1" || e.Timestamp.IsZero() {
			t.Fatalf("event %+v", e)
		}
	}
	want := []string{"limit_hit/w@x.com", "limit_hit/p@x.com", "pool_exhausted/p@x.com"}
	if !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}
