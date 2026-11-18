package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func payload(m map[string]any) []byte {
	m["session_id"] = "sid-123"
	b, _ := json.Marshal(m)
	return b
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name  string
		event string
		p     map[string]any
		want  Kind
	}{
		{"structured rate_limit", "StopFailure", map[string]any{"error": "rate_limit", "error_details": "429 Too Many Requests"}, RateLimited},
		{"prose session limit with uninformative error", "StopFailure", map[string]any{"error": "error", "last_assistant_message": "You've hit your session limit · resets 7pm"}, RateLimited},
		{"weekly limit prose", "StopFailure", map[string]any{"error": "unknown", "last_assistant_message": "You've hit your weekly limit"}, RateLimited},
		{"overloaded alone is not account usage", "StopFailure", map[string]any{"error": "overloaded"}, None},
		{"context window is not a usage limit", "StopFailure", map[string]any{"error": "invalid_request", "last_assistant_message": "context window exceeded"}, None},
		{"web page mentioning rate limit", "StopFailure", map[string]any{"error": "unknown", "last_assistant_message": "<html><body>rate limit docs</body></html>"}, None},
		{"bare 429 without api context", "StopFailure", map[string]any{"error": "unknown", "last_assistant_message": "the number 429"}, None},
		{"429 with api context", "StopFailure", map[string]any{"error": "unknown", "error_details": "API Error: 429 Too Many Requests"}, RateLimited},
		{"stop", "Stop", map[string]any{"last_assistant_message": "done"}, Stopped},
		{"stop mentioning a limit is still just a stop", "Stop", map[string]any{"last_assistant_message": "I hit your weekly limit in the example"}, Stopped},
		{"prompt", "UserPromptSubmit", map[string]any{"prompt": "hi"}, PromptSubmitted},
	}
	for _, c := range cases {
		s := Classify(c.event, payload(c.p))
		if s.Kind != c.want {
			t.Errorf("%s: kind = %q, want %q", c.name, s.Kind, c.want)
		}
		if c.want != None && s.SessionID != "sid-123" {
			t.Errorf("%s: session id = %q", c.name, s.SessionID)
		}
	}
}

// procTree fakes the process table: pid -> (ppid, comm).
type proc struct {
	ppid int
	comm string
}

// own makes dir belong to a supervised Claude (pid 100) and puts this hook
// under it as hook(self) <- sh(50) <- claude(100).
func own(t *testing.T, dir string) {
	t.Helper()
	tree(t, dir, 100, map[int]proc{50: {100, "sh"}, 100: {1, "claude"}}, 50)
}

func tree(t *testing.T, dir string, child int, procs map[int]proc, parent int) {
	t.Helper()
	_ = os.WriteFile(filepath.Join(dir, ChildPIDFile), []byte(strconv.Itoa(child)), 0o600)
	o1, o2 := selfParent, procInfo
	t.Cleanup(func() { selfParent, procInfo = o1, o2 })
	selfParent = func() int { return parent }
	procInfo = func(pid int) (int, string, error) {
		p, ok := procs[pid]
		if !ok {
			return 0, "", os.ErrNotExist
		}
		return p.ppid, p.comm, nil
	}
}

func env(dir string) func(string) string {
	return func(k string) string {
		if k == "SWITCCH_SIGNAL_DIR" {
			return dir
		}
		return ""
	}
}

func TestHandleHookInertOutsideSwitcch(t *testing.T) {
	code, msg := HandleHook("StopFailure", strings.NewReader(string(payload(map[string]any{"error": "rate_limit"}))), env(""))
	if code != 0 || msg != "" {
		t.Fatalf("outside a switcch session the hook must be inert, got %d %q", code, msg)
	}
}

func TestHandleHookPromptPassesWhenNotArmed(t *testing.T) {
	dir := t.TempDir()
	own(t, dir)
	code, msg := HandleHook("UserPromptSubmit", strings.NewReader(string(payload(map[string]any{"prompt": "hello"}))), env(dir))
	if code != 0 || msg != "" {
		t.Fatalf("unarmed prompt must pass untouched, got %d %q", code, msg)
	}
	if _, err := os.Stat(filepath.Join(dir, "intercept")); !os.IsNotExist(err) {
		t.Fatal("no intercept signal may be written when not armed")
	}
	if _, err := os.Stat(filepath.Join(dir, "busy")); err != nil {
		t.Fatal("a prompt must mark the chat busy")
	}
}

func TestHandleHookInterceptsWhenArmed(t *testing.T) {
	dir := t.TempDir()
	own(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "armed"), []byte("personal"), 0o600); err != nil {
		t.Fatal(err)
	}
	prompt := "line one\nwith \"quotes\" and unicode ✓"
	code, msg := HandleHook("UserPromptSubmit", strings.NewReader(string(payload(map[string]any{"prompt": prompt}))), env(dir))
	if code != 2 || !strings.Contains(msg, "personal") {
		t.Fatalf("armed prompt must be blocked (exit 2) with a notice, got %d %q", code, msg)
	}
	s, err := ReadSignal(dir, Intercept)
	if err != nil {
		t.Fatal(err)
	}
	if s.Prompt != prompt || s.SessionID != "sid-123" {
		t.Fatalf("intercept signal = %+v", s)
	}
}

func TestHandleHookStopAndRateLimitSignals(t *testing.T) {
	dir := t.TempDir()
	own(t, dir)
	_ = os.WriteFile(filepath.Join(dir, "busy"), nil, 0o600)
	HandleHook("Stop", strings.NewReader(string(payload(map[string]any{}))), env(dir))
	if _, err := os.Stat(filepath.Join(dir, "busy")); !os.IsNotExist(err) {
		t.Fatal("Stop must clear the busy marker")
	}
	if _, err := ReadSignal(dir, Stopped); err != nil {
		t.Fatal("Stop must write a stopped signal")
	}
	HandleHook("StopFailure", strings.NewReader(string(payload(map[string]any{"error": "rate_limit"}))), env(dir))
	if s, err := ReadSignal(dir, RateLimited); err != nil || s.SessionID != "sid-123" {
		t.Fatalf("rate-limited signal: %+v %v", s, err)
	}
}

func TestHookIgnoresNestedClaude(t *testing.T) {
	// The supervised Claude (100) runs a Bash tool (zsh 60) that runs another
	// claude (70); that nested claude fires the same hooks with the inherited
	// SWITCCH_SIGNAL_DIR. Its hooks must not drive the supervisor.
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ArmedFile), []byte("personal"), 0o600)
	tree(t, dir, 100, map[int]proc{
		50: {70, "sh"}, 70: {60, "claude"}, 60: {100, "zsh"}, 100: {1, "claude"},
	}, 50)
	if code, msg := HandleHook("UserPromptSubmit", strings.NewReader(string(payload(map[string]any{"prompt": "x"}))), env(dir)); code != 0 || msg != "" {
		t.Fatalf("a nested claude's prompt must pass untouched, got %d %q", code, msg)
	}
	HandleHook("Stop", strings.NewReader(string(payload(map[string]any{}))), env(dir))
	for _, f := range []string{string(Intercept), string(Stopped)} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Fatalf("nested claude must not write %s", f)
		}
	}
}

func TestHookDirectChildWithoutShell(t *testing.T) {
	dir := t.TempDir()
	tree(t, dir, 100, map[int]proc{100: {1, "claude"}}, 100)
	HandleHook("Stop", strings.NewReader(string(payload(map[string]any{}))), env(dir))
	if _, err := ReadSignal(dir, Stopped); err != nil {
		t.Fatal("a hook run directly by the supervised claude must act")
	}
}

func TestStopFailureAlwaysClearsBusy(t *testing.T) {
	dir := t.TempDir()
	own(t, dir)
	_ = os.WriteFile(filepath.Join(dir, BusyFile), nil, 0o600)
	HandleHook("StopFailure", strings.NewReader(string(payload(map[string]any{"error": "overloaded"}))), env(dir))
	if _, err := os.Stat(filepath.Join(dir, BusyFile)); !os.IsNotExist(err) {
		t.Fatal("any StopFailure ends the turn: busy must be cleared")
	}
}

func TestClassifyModelCap(t *testing.T) {
	cases := []struct {
		name  string
		p     map[string]any
		model string
	}{
		{"opus cap", map[string]any{"error": "error", "last_assistant_message": "You've reached your Opus limit · resets Mon"}, "opus"},
		{"sonnet weekly cap", map[string]any{"error": "error", "last_assistant_message": "Sonnet weekly limit reached"}, "sonnet"},
		{"account-wide 429 naming a model", map[string]any{"error": "rate_limit", "error_details": "429 Too Many Requests (model: claude-opus-4)"}, ""},
		{"plain session limit", map[string]any{"error": "error", "last_assistant_message": "You've hit your session limit"}, ""},
	}
	for _, c := range cases {
		s := Classify("StopFailure", payload(c.p))
		if s.Kind != RateLimited || s.Model != c.model {
			t.Errorf("%s: got kind=%q model=%q, want rate-limited model=%q", c.name, s.Kind, s.Model, c.model)
		}
	}
}
