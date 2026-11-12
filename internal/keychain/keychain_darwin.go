//go:build darwin

package keychain

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrNotFound is returned by Read when the Keychain has no matching item
// (security exit code 44), as distinct from a locked/denied/unavailable Keychain.
var ErrNotFound = errors.New("keychain: item not found")

const securityBin = "/usr/bin/security"

// Read returns the credential stored under service for the current account, or
// ErrNotFound if absent. Any other non-zero exit is reported as an error so a
// locked Keychain is never mistaken for a missing item.
func Read(service string) (string, error) {
	cmd := exec.Command(securityBin, "find-generic-password", "-a", AccountName(), "-w", "-s", service)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err == nil {
		return strings.TrimRight(out.String(), "\n"), nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ee.ExitCode() == 44 {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("keychain: read %q failed (exit %d): %s", service, ee.ExitCode(), strings.TrimSpace(errb.String()))
	}
	return "", fmt.Errorf("keychain: read %q: %w", service, err)
}

// Write stores value under service for the current account, replacing any
// existing item. The value is hex-encoded and fed via `security -i` stdin so the
// payload never appears in process arguments (ps).
func Write(service, value string) error {
	acct := AccountName()
	hexVal := hex.EncodeToString([]byte(value))
	line := fmt.Sprintf("add-generic-password -U -a %q -s %q -X %s\n", acct, service, hexVal)
	// security's interactive line buffer caps at ~4096 bytes. We deliberately do
	// NOT fall back to argv for larger payloads: a secret on argv is visible in
	// ps. A single-account credential fits comfortably; if this is ever hit, the
	// fix is the Security framework (SecItemAdd via cgo), not argv exposure.
	if len(line) > 4096 {
		return fmt.Errorf("keychain: write %q: payload too large for secure path (%d bytes); refusing to expose it on argv", service, len(line))
	}
	cmd := exec.Command(securityBin, "-i")
	cmd.Stdin = strings.NewReader(line)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("keychain: write %q failed: %v: %s", service, err, strings.TrimSpace(errb.String()))
	}
	return nil
}

// Delete removes the item under service. A missing item (exit 44) is not an error.
func Delete(service string) error {
	cmd := exec.Command(securityBin, "delete-generic-password", "-s", service)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	err := cmd.Run()
	if err == nil {
		return nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 44 {
		return nil
	}
	return fmt.Errorf("keychain: delete %q failed: %v: %s", service, err, strings.TrimSpace(errb.String()))
}
