//go:build darwin

package hooks

import (
	"bytes"

	"golang.org/x/sys/unix"
)

// procInfoSys returns pid's parent pid and command name from the kernel
// (a sysctl — no exec, so it is cheap enough to run on every prompt).
func procInfoSys(pid int) (int, string, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, "", err
	}
	comm := kp.Proc.P_comm[:]
	if i := bytes.IndexByte(comm, 0); i >= 0 {
		comm = comm[:i]
	}
	return int(kp.Eproc.Ppid), string(comm), nil
}
