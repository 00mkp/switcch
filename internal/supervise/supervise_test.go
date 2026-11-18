//go:build darwin

package supervise

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/00mkp/switcch/internal/account"
	"github.com/00mkp/switcch/internal/config"
	"github.com/00mkp/switcch/internal/hooks"
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
	got := next(pool, work, usageMap(map[int]float64{2: 100, 3: 10}), 100)
	if got == nil || got.Slot != other.Slot {
		t.Fatalf("next = %+v, want other (personal is exhausted)", got)
	}
	if got := next([]account.Account{personal}, personal, usageMap(nil), 100); got != nil {
		t.Fatalf("a pin has no next account, got %+v", got)
	}
	if got := next(pool, work, usageMap(map[int]float64{2: 95, 3: 95}), 90); got != nil {
		t.Fatalf("proactive limit must exclude accounts already over it, got %+v", got)
	}
}

func TestDecide(t *testing.T) {
	cases := []struct {
		ev   event
		want action
	}{
		{event{kind: hooks.RateLimited, hasTarget: true, mode: "auto"}, switchAcct},
		{event{kind: hooks.RateLimited, hasTarget: false, mode: "auto"}, stay},
		{event{kind: hooks.RateLimited, hasTarget: true, mode: "advisory"}, stay},
		{event{kind: hooks.Intercept, hasTarget: true, mode: "auto"}, switchWithPrompt},
		{event{exited: true}, done},
	}
	for _, c := range cases {
		if got := decide(c.ev); got != c.want {
			t.Errorf("decide(%+v) = %v, want %v", c.ev, got, c.want)
		}
	}
}

func TestRelaunchArgsStripsSessionFlags(t *testing.T) {
	got := relaunchArgs([]string{"--model", "opus", "--resume", "old-id", "--continue", "-c"}, "sid-9", "hello")
	want := []string{"--model", "opus", "--resume", "sid-9", "hello"}
	if !slices.Equal(got, want) {
		t.Fatalf("relaunchArgs = %v, want %v", got, want)
	}
	if got := relaunchArgs(nil, "", "hi"); !slices.Equal(got, []string{"--continue", "hi"}) {
		t.Fatalf("no session id should fall back to --continue, got %v", got)
	}
}

// ---- fake claude ----

type launchRec struct {
	Args      []string `json:"args"`
	ConfigDir string   `json:"config_dir"`
	Watchdog  string   `json:"watchdog"`
}

// TestHelperProcess is the fake `claude`. Launch 1 emits the configured signal
// (as the hook would) and blocks until SIGTERM; later launches exit at once.
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
	logp := os.Getenv("FAKE_LOG")
	rec, _ := json.Marshal(launchRec{Args: args, ConfigDir: os.Getenv("CLAUDE_CONFIG_DIR"), Watchdog: os.Getenv("CLAUDE_CODE_RETRY_WATCHDOG")})
	f, _ := os.OpenFile(logp, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	f.Write(append(rec, '\n'))
	f.Close()
	n := countLines(logp)
	if n == 1 && os.Getenv("FAKE_ACT") != "none" {
		term := make(chan os.Signal, 1)
		signal.Notify(term, syscall.SIGTERM)
		dir := os.Getenv("SWITCCH_SIGNAL_DIR")
		switch os.Getenv("FAKE_ACT") {
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
		case <-time.After(wait):
		}
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

func runFake(t *testing.T, act, prompt string, pool []account.Account) []launchRec {
	t.Helper()
	logp := filepath.Join(t.TempDir(), "launches.jsonl")
	o1, o2, o3, o4, o5, o6 := prepare, usageOf, captureBack, command, claudePath, pollInterval
	t.Cleanup(func() { prepare, usageOf, captureBack, command, claudePath, pollInterval = o1, o2, o3, o4, o5, o6 })
	prepare = func(a account.Account) (string, []string, error) {
		dir := "/profiles/" + a.Alias
		return dir, []string{
			"CLAUDE_CONFIG_DIR=" + dir, "CLAUDE_CODE_RETRY_WATCHDOG=1",
			"SWITCCH_FAKE_CLAUDE=1", "FAKE_LOG=" + logp, "FAKE_ACT=" + act, "FAKE_PROMPT=" + prompt,
			"FAKE_WAIT=300ms",
		}, nil
	}
	usageOf = usageMap(nil)
	captureBack = func(account.Account) {}
	claudePath = func() (string, error) { return os.Args[0], nil }
	command = func(name string, args ...string) *exec.Cmd {
		return exec.Command(name, append([]string{"-test.run=TestHelperProcess", "--"}, args...)...)
	}
	pollInterval = 10 * time.Millisecond

	cfg := config.Defaults()
	err := Run(context.Background(), Options{Pool: pool, Config: cfg, RuntimeDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return readLaunches(t, logp)
}

func TestRunRateLimitFailsOverToSameConversation(t *testing.T) {
	l := runFake(t, "ratelimit", "", []account.Account{work, personal})
	if len(l) != 2 {
		t.Fatalf("want 2 launches, got %d: %+v", len(l), l)
	}
	if l[1].ConfigDir != "/profiles/personal" {
		t.Fatalf("relaunch should be on personal's profile, got %q", l[1].ConfigDir)
	}
	a := l[1].Args
	if len(a) < 3 || a[len(a)-3] != "--resume" || a[len(a)-2] != "sid-1" {
		t.Fatalf("relaunch must resume the same session, got %v", a)
	}
	if msg := a[len(a)-1]; !strings.Contains(msg, "work") || !strings.Contains(msg, "personal") {
		t.Fatalf("notice should name both accounts, got %q", msg)
	}
	if l[0].Watchdog != "" || l[1].Watchdog != "" {
		t.Fatal("CLAUDE_CODE_RETRY_WATCHDOG must be unset in the child (it can suppress StopFailure)")
	}
}

func TestRunInterceptCarriesPromptVerbatim(t *testing.T) {
	prompt := "line one\nwith \"quotes\" and unicode ✓"
	l := runFake(t, "intercept", prompt, []account.Account{work, personal})
	if len(l) != 2 {
		t.Fatalf("want 2 launches, got %d", len(l))
	}
	last := l[1].Args[len(l[1].Args)-1]
	if !strings.HasSuffix(last, "\n\n"+prompt) {
		t.Fatalf("intercepted prompt must arrive verbatim after the notice, got %q", last)
	}
	if l[1].ConfigDir != "/profiles/personal" {
		t.Fatalf("intercept should move to personal, got %q", l[1].ConfigDir)
	}
}

func TestRunUserExitEndsWithNoRelaunch(t *testing.T) {
	l := runFake(t, "none", "", []account.Account{work, personal})
	if len(l) != 1 {
		t.Fatalf("a normal exit must end switcch with no relaunch, got %d launches", len(l))
	}
}

func TestRunPinnedRateLimitDoesNotLoop(t *testing.T) {
	// A pin has nowhere to go: the limit signal must not cause a relaunch; Claude
	// keeps showing its limit until it exits on its own.
	l := runFake(t, "ratelimit", "", []account.Account{personal})
	if len(l) != 1 {
		t.Fatalf("a pinned chat must not be relaunched on a limit, got %d launches", len(l))
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
	logp := filepath.Join(t.TempDir(), "launches.jsonl")
	root = t.TempDir()
	o := []any{prepare, usageOf, captureBack, command, claudePath, pollInterval, usagePollInterval, notify, clock}
	t.Cleanup(func() {
		prepare, usageOf, captureBack, command, claudePath = o[0].(func(account.Account) (string, []string, error)), o[1].(func(account.Account) (usage.Usage, error)), o[2].(func(account.Account)), o[3].(func(string, ...string) *exec.Cmd), o[4].(func() (string, error))
		pollInterval, usagePollInterval, notify, clock = o[5].(time.Duration), o[6].(time.Duration), o[7].(func(string, string)), o[8].(func() time.Time)
	})
	prepare = func(a account.Account) (string, []string, error) {
		dir := "/profiles/" + a.Alias
		return dir, []string{"CLAUDE_CONFIG_DIR=" + dir, "SWITCCH_FAKE_CLAUDE=1", "FAKE_LOG=" + logp, "FAKE_ACT=block", "FAKE_WAIT=700ms"}, nil
	}
	usageOf = func(a account.Account) (usage.Usage, error) {
		if a.Slot == pool[0].Slot {
			polls++
		}
		return usage.Usage{FiveHour: usage.Window{Utilization: util[a.Slot]}}, nil
	}
	captureBack = func(account.Account) {}
	claudePath = func() (string, error) { return os.Args[0], nil }
	command = func(name string, args ...string) *exec.Cmd {
		return exec.Command(name, append([]string{"-test.run=TestHelperProcess", "--"}, args...)...)
	}
	pollInterval = 5 * time.Millisecond
	usagePollInterval = 20 * time.Millisecond
	notify = func(title, msg string) { notes = append(notes, msg) }
	// A fake clock that runs 1000x fast so idle minutes elapse in milliseconds.
	start := time.Now()
	clock = func() time.Time { return start.Add(time.Since(start) * 1000) }

	go func() {
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

// scriptedArgs are the user's claude args for the next scripted run.
var scriptedArgs []string

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
	a := l[1].Args
	if a[len(a)-3] != "--resume" || a[len(a)-2] != "sid-1" {
		t.Fatalf("must resume the same chat, got %v", a)
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
	if a := l[1].Args; a[len(a)-3] != "--resume" || a[len(a)-2] != "sid-1" {
		t.Fatalf("idle migration must resume the same chat, got %v", a)
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
	if len(l) != 2 || !strings.HasSuffix(l[1].Args[len(l[1].Args)-1], "\n\nlate msg") {
		t.Fatalf("late prompt must be delivered on the new account, got %+v", l)
	}
}
