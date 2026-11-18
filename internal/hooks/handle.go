package hooks

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Kind is a signal the hook passes to the supervising switcch process.
type Kind string

const (
	None            Kind = ""
	RateLimited     Kind = "rate-limited"
	Stopped         Kind = "stopped"
	PromptSubmitted Kind = "prompt-submitted"
	Intercept       Kind = "intercept"
)

// Signal is what a hook observed.
type Signal struct {
	Kind      Kind   `json:"kind"`
	SessionID string `json:"session_id"`
	Prompt    string `json:"prompt,omitempty"`
}

// Marker files in the signal dir (written by the supervisor or the hook).
const (
	ArmedFile = "armed" // supervisor: this chat's account is over the line; content = next account name
	BusyFile  = "busy"  // hook: a turn is in progress (prompt submitted, no Stop yet)
)

type hookPayload struct {
	SessionID            string `json:"session_id"`
	Prompt               string `json:"prompt"`
	Error                string `json:"error"`
	ErrorDetails         string `json:"error_details"`
	LastAssistantMessage string `json:"last_assistant_message"`
}

// Conservative rules ported from cux: a false positive restarts a healthy chat.
var (
	foreignMarkers = []string{"url: http", "<!doctype", "<html", "<head", "<body"}
	denylist       = []string{"do not retry", "concurrent subagent", "context window", "token limit", "output limit", "max tokens"}
	strongPhrases  = []string{"usage limit", "session limit", "hit your limit", "reached your limit", "weekly limit", "limit will reset", "upgrade to increase your usage", "opus limit", "sonnet limit"}
	weakPhrases    = []string{"429", "too many requests", "quota exceeded", "rate limit", "rate_limit_error"}
	apiContext     = []string{"api", "anthropic", "claude", "http", "status", "response", "retry-after"}
)

func containsAny(s string, subs []string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

// Classify maps a hook event + payload to a signal.
func Classify(event string, raw []byte) Signal {
	var p hookPayload
	_ = json.Unmarshal(raw, &p)
	switch event {
	case "UserPromptSubmit":
		return Signal{Kind: PromptSubmitted, SessionID: p.SessionID, Prompt: p.Prompt}
	case "Stop":
		return Signal{Kind: Stopped, SessionID: p.SessionID}
	case "StopFailure":
		text := strings.ToLower(p.Error + " " + p.ErrorDetails + " " + p.LastAssistantMessage)
		if containsAny(text, foreignMarkers) || containsAny(text, denylist) {
			return Signal{}
		}
		limited := p.Error == "rate_limit" ||
			containsAny(text, strongPhrases) ||
			(containsAny(text, weakPhrases) && containsAny(text, apiContext))
		if limited {
			return Signal{Kind: RateLimited, SessionID: p.SessionID}
		}
	}
	return Signal{}
}

// WriteSignal atomically writes s to <dir>/<kind>.
func WriteSignal(dir string, s Signal) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	p := filepath.Join(dir, string(s.Kind))
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ReadSignal reads <dir>/<kind> without consuming it.
func ReadSignal(dir string, k Kind) (Signal, error) {
	b, err := os.ReadFile(filepath.Join(dir, string(k)))
	if err != nil {
		return Signal{}, err
	}
	var s Signal
	err = json.Unmarshal(b, &s)
	return s, err
}

// HandleHook runs one hook invocation. Outside a switcch session
// (SWITCCH_SIGNAL_DIR unset) it is inert. It returns the process exit code and
// any stderr message; exit 2 on UserPromptSubmit blocks the prompt.
func HandleHook(event string, stdin io.Reader, getenv func(string) string) (int, string) {
	dir := getenv("SWITCCH_SIGNAL_DIR")
	if dir == "" {
		return 0, ""
	}
	raw, _ := io.ReadAll(stdin)
	s := Classify(event, raw)
	switch s.Kind {
	case PromptSubmitted:
		next, err := os.ReadFile(filepath.Join(dir, ArmedFile))
		if err != nil { // not armed: let the prompt through untouched
			_ = os.WriteFile(filepath.Join(dir, BusyFile), nil, 0o600)
			return 0, ""
		}
		if err := WriteSignal(dir, Signal{Kind: Intercept, SessionID: s.SessionID, Prompt: s.Prompt}); err != nil {
			// Couldn't hand the prompt over — never eat it; let it through.
			_ = os.WriteFile(filepath.Join(dir, BusyFile), nil, 0o600)
			return 0, ""
		}
		return 2, "switcch: moving this chat to " + strings.TrimSpace(string(next)) + " — your message will be sent there"
	case Stopped:
		_ = os.Remove(filepath.Join(dir, BusyFile))
		_ = WriteSignal(dir, s)
	case RateLimited:
		_ = WriteSignal(dir, s)
	}
	return 0, ""
}
