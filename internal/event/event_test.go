package event

import (
	"encoding/json"
	"os"
	"path/filepath"
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
