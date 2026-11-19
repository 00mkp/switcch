// Package event emits switcch's one interop event (spec §5.9): a JSON payload
// handed to a user-configured command when a chat hits a limit or runs out of
// accounts, so a future cross-provider router can react. switcch contains no
// router logic itself.
package event

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"syscall"
	"time"
)

// Reasons.
const (
	LimitHit      = "limit_hit"      // the chat's active account hit a usage limit
	PoolExhausted = "pool_exhausted" // no account in the chat's pool can take it
)

// Event is the payload, exactly {session_id, account, reason, timestamp}.
type Event struct {
	SessionID string    `json:"session_id"`
	Account   string    `json:"account"` // the account's email
	Reason    string    `json:"reason"`
	Timestamp time.Time `json:"timestamp"`
}

// Timeout bounds one event command.
var Timeout = 10 * time.Second

// Emit runs command (via sh -c, as the user wrote it — like a git hook) with e
// as one line of JSON on stdin. It returns at once; the command's output is
// discarded and it is killed after Timeout. Event data is never interpolated
// into the command line. command == "" is a no-op.
func Emit(command string, e Event) {
	if command == "" {
		return
	}
	e.Timestamp = e.Timestamp.UTC().Truncate(time.Second)
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), Timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
		cmd.Stdin = bytes.NewReader(append(b, '\n'))
		// Own process group, killed as a whole on timeout: a compound command's
		// children must not outlive it.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
		cmd.WaitDelay = time.Second
		_ = cmd.Run() // stdout/stderr nil = discarded (never onto Claude's screen)
	}()
}
