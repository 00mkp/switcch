package event

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestEmitPayload(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	ts := time.Date(2026, 11, 18, 9, 30, 0, 0, time.UTC)
	Emit("cat > '"+out+"'", Event{SessionID: "sid-1", Account: "w@x.com", Reason: LimitHit, Timestamp: ts})
	var b []byte
	for i := 0; i < 100 && len(b) == 0; i++ {
		time.Sleep(20 * time.Millisecond)
		b, _ = os.ReadFile(out)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("payload %q: %v", b, err)
	}
	want := map[string]any{"session_id": "sid-1", "account": "w@x.com", "reason": "limit_hit", "timestamp": "2026-11-18T09:30:00Z"}
	if len(m) != len(want) {
		t.Fatalf("payload must have exactly %v, got %v", want, m)
	}
	for k, v := range want {
		if m[k] != v {
			t.Fatalf("%s = %v, want %v", k, m[k], v)
		}
	}
}

func TestEventCommandNeverBlocks(t *testing.T) {
	start := time.Now()
	Emit("sleep 30; echo x", Event{Reason: PoolExhausted})
	Emit("", Event{Reason: PoolExhausted})
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("Emit blocked for %v", d)
	}
}

func TestTimeoutKillsTheWholeCommand(t *testing.T) {
	o := Timeout
	t.Cleanup(func() { Timeout = o })
	Timeout = 200 * time.Millisecond
	pidFile := filepath.Join(t.TempDir(), "pid")
	// A compound command whose child would outlive a kill of sh alone.
	Emit("sleep 30 & echo $! > '"+pidFile+"'; wait", Event{Reason: LimitHit})
	var pid int
	for i := 0; i < 100 && pid == 0; i++ {
		time.Sleep(10 * time.Millisecond)
		b, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	if pid == 0 {
		t.Fatal("command never started")
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return // gone
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d from the event command outlived the timeout", pid)
}
