//go:build unix

package session

import (
	"errors"
	"syscall"
)

// processAlive reports whether pid names a running process. Signal 0 checks
// existence without delivering anything; EPERM means the process exists but
// belongs to another user, which still counts.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
