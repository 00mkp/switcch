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
